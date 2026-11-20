package lab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const gib = int64(1) << 30

// Atualizador baixa edições novas do acervo enquanto o programa está
// aberto: coleções Kiwix por semana e mapas por mês. Sem internet ou sem
// espaço, adia e mantém o que já está instalado.
type Atualizador struct {
	cfg   *Config
	kiwix *Processo
	rotas *Processo
	http  *http.Client

	// antesDeTrocarBusca fecha o índice em uso; no Windows não dá para
	// substituir arquivo aberto.
	antesDeTrocarBusca func()

	mu       sync.Mutex
	baixando map[string]*andamento
	estado   string
	detalhe  string
}

type andamento struct {
	texto string
	pct   *int
}

func NovoAtualizador(cfg *Config, kiwix, rotas *Processo) *Atualizador {
	return &Atualizador{cfg: cfg, kiwix: kiwix, rotas: rotas, http: &http.Client{Timeout: 0},
		baixando: map[string]*andamento{}, estado: "pendente", detalhe: "Primeira conferência em instantes."}
}

func (a *Atualizador) progresso(arquivo string) *andamento {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.baixando[arquivo]
}

func (a *Atualizador) resumo() parte {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.cfg.Atualizar {
		return parte{"parado", "Atualização automática desligada em config/lab.json."}
	}
	return parte{a.estado, a.detalhe}
}

func (a *Atualizador) marcar(e, d string) {
	a.mu.Lock()
	a.estado, a.detalhe = e, d
	a.mu.Unlock()
	log.Printf("atualização: %s", d)
}

type registroAtualizacao struct {
	UltimaConferencia map[string]time.Time `json:"ultima_conferencia"`
}

func (a *Atualizador) registro() registroAtualizacao {
	var r registroAtualizacao
	b, _ := os.ReadFile(a.cfg.Caminho("estado", "atualizacoes-lab.json"))
	_ = json.Unmarshal(b, &r)
	if r.UltimaConferencia == nil {
		r.UltimaConferencia = map[string]time.Time{}
	}
	return r
}

func (a *Atualizador) gravarRegistro(r registroAtualizacao) {
	b, _ := json.MarshalIndent(r, "", "  ")
	_ = os.MkdirAll(a.cfg.Caminho("estado"), 0o755)
	tmp := a.cfg.Caminho("estado", "atualizacoes-lab.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, a.cfg.Caminho("estado", "atualizacoes-lab.json"))
	}
}

// Rodar confere logo no início (se faltar coleção) e depois todo dia na
// hora configurada.
func (a *Atualizador) Rodar(ctx context.Context) {
	espera := 2 * time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(espera):
		}
		a.Executar(ctx)
		agora := time.Now()
		prox := time.Date(agora.Year(), agora.Month(), agora.Day(), a.cfg.HoraAtualizacao, 0, 0, 0, agora.Location())
		if !prox.After(agora) {
			prox = prox.Add(24 * time.Hour)
		}
		espera = time.Until(prox)
		a.mu.Lock()
		a.detalhe += " Próxima conferência " + prox.Format("02/01 às 15h04") + "."
		a.mu.Unlock()
	}
}

func (a *Atualizador) get(ctx context.Context, url string, limite int64) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "offline-library/"+Versao)
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	resp, err := a.http.Do(req.WithContext(c))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limite))
}

// Executar faz uma rodada completa. Erros de uma coleção não param as outras.
func (a *Atualizador) Executar(ctx context.Context) {
	if _, err := a.get(ctx, "https://download.kiwix.org/zim/", 1<<16); err != nil {
		a.marcar("pendente", "Sem internet; acervo atual mantido.")
		return
	}
	reg := a.registro()
	falhas, novas := 0, 0
	if err := a.reporRuntime(ctx); err != nil {
		falhas++
		log.Printf("atualização: programas de apoio: %v", err)
	}
	// Busca e mapas antes das coleções: um download grande de enciclopédia
	// pode levar dias e não deve segurar o resto.
	if err := a.atualizarBusca(ctx, &reg); err != nil {
		falhas++
		log.Printf("atualização: busca de endereços: %v", err)
	}
	if err := a.atualizarMapas(ctx, &reg); err != nil {
		falhas++
		log.Printf("atualização: mapas: %v", err)
	}
	instalados := map[string]Zim{}
	for _, z := range ZimsProntos(a.cfg) {
		instalados[z.Prefixo] = z
	}
	for _, item := range LerAcervo(a.cfg) {
		if ctx.Err() != nil {
			return
		}
		atual, tem := instalados[item.Prefixo]
		if tem && time.Since(reg.UltimaConferencia[item.Prefixo]) < 7*24*time.Hour {
			continue
		}
		nova, err := a.ultimaEdicao(ctx, item)
		if err != nil {
			falhas++
			log.Printf("atualização: %s: %v", item.Prefixo, err)
			continue
		}
		if tem && nova <= atual.Arquivo {
			reg.UltimaConferencia[item.Prefixo] = time.Now()
			a.gravarRegistro(reg)
			continue
		}
		if err := a.baixarZim(ctx, item.Categoria, nova); err != nil {
			falhas++
			log.Printf("atualização: %s: %v", nova, err)
			continue
		}
		novas++
		reg.UltimaConferencia[item.Prefixo] = time.Now()
		a.gravarRegistro(reg)
		if tem {
			_ = os.Remove(atual.Caminho)
			_ = os.Remove(atual.Caminho + ".ok")
		}
	}
	estado, texto := "pronto", "Acervo conferido."
	if novas > 0 {
		texto = fmt.Sprintf("Acervo conferido, %d coleções novas instaladas.", novas)
	}
	if falhas > 0 {
		estado, texto = "falha", fmt.Sprintf("%s %d itens com falha; nova tentativa na próxima rodada.", texto, falhas)
	}
	a.marcar(estado, texto)
}

var reHref = regexp.MustCompile(`href="([^"]+\.zim)"`)

func (a *Atualizador) ultimaEdicao(ctx context.Context, item ItemAcervo) (string, error) {
	b, err := a.get(ctx, "https://download.kiwix.org/zim/"+item.Categoria+"/", 32<<20)
	if err != nil {
		return "", err
	}
	var achados []string
	for _, m := range reHref.FindAllStringSubmatch(string(b), -1) {
		nome := filepath.Base(m[1])
		if pre, _, ok := analisarZim(nome); ok && pre == item.Prefixo {
			achados = append(achados, nome)
		}
	}
	if len(achados) == 0 {
		return "", errors.New("coleção não encontrada no catálogo do Kiwix")
	}
	sort.Strings(achados)
	return achados[len(achados)-1], nil
}

type meta4 struct {
	Arquivos []struct {
		Nome    string `xml:"name,attr"`
		Tamanho int64  `xml:"size"`
		Hashes  []struct {
			Tipo  string `xml:"type,attr"`
			Valor string `xml:",chardata"`
		} `xml:"hash"`
		Pedacos struct {
			Tamanho int64    `xml:"length,attr"`
			Tipo    string   `xml:"type,attr"`
			Hashes  []string `xml:"hash"`
		} `xml:"pieces"`
	} `xml:"file"`
}

// baixarZim baixa com retomada, confere tamanho e SHA-256 oficiais e só
// então publica com o marcador .ok.
func (a *Atualizador) baixarZim(ctx context.Context, categoria, nome string) error {
	url := "https://download.kiwix.org/zim/" + categoria + "/" + nome
	dl := a.cfg.Caminho("downloads")
	controle := filepath.Join(dl, nome+".aria2")
	if st, err := os.Stat(controle); err == nil {
		// O aria2 grava o controle a cada minuto. Parado há horas, o
		// download foi abandonado e o arquivo parcial é adotado.
		if time.Since(st.ModTime()) < 3*time.Hour {
			return errors.New("outro programa está baixando esta edição; aguardando")
		}
		if err := os.Rename(filepath.Join(dl, nome), filepath.Join(dl, nome+".lab-parcial")); err == nil || errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(controle)
			log.Printf("atualização: %s: download abandonado do aria2 adotado", nome)
		}
	}
	b, err := a.get(ctx, url+".meta4", 4<<20)
	if err != nil {
		return err
	}
	var m meta4
	if err := xml.Unmarshal(b, &m); err != nil || len(m.Arquivos) == 0 || m.Arquivos[0].Nome != nome {
		return errors.New("metadados oficiais inválidos")
	}
	tamanho := m.Arquivos[0].Tamanho
	resumo := ""
	for _, h := range m.Arquivos[0].Hashes {
		if h.Tipo == "sha-256" {
			resumo = strings.TrimSpace(h.Valor)
		}
	}
	if tamanho <= 0 || len(resumo) != 64 {
		return errors.New("tamanho ou resumo oficial ausente")
	}
	_ = os.MkdirAll(dl, 0o755)
	parcial := filepath.Join(dl, nome+".lab-parcial")
	var feito int64
	if st, err := os.Stat(parcial); err == nil {
		feito = st.Size()
	}
	if livre, _, err := EspacoDisco(a.cfg.Dados); err == nil && int64(livre) < tamanho-feito+5*gib {
		return fmt.Errorf("espaço insuficiente: faltam %s", tamanhoLegivel(tamanho-feito+5*gib-int64(livre)))
	}
	defer func() { a.mu.Lock(); delete(a.baixando, nome); a.mu.Unlock() }()
	p := m.Arquivos[0].Pedacos
	if p.Tipo == "sha-1" && p.Tamanho > 0 && int64(len(p.Hashes)) == (tamanho+p.Tamanho-1)/p.Tamanho {
		if err := a.baixarPorPedacos(ctx, url, parcial, nome, tamanho, resumo, p.Tamanho, p.Hashes); err != nil {
			return err
		}
	} else {
		if feito < tamanho {
			if err := a.baixarRetomando(ctx, url, parcial, nome, feito, tamanho); err != nil {
				return err
			}
		}
		a.mu.Lock()
		a.baixando[nome] = &andamento{texto: "Conferindo o resumo SHA-256 oficial."}
		a.mu.Unlock()
		soma, err := sha256Arquivo(parcial)
		if err != nil {
			return err
		}
		if soma != resumo {
			_ = os.Rename(parcial, parcial+".rejeitado")
			return errors.New("resumo SHA-256 diferente do oficial; arquivo rejeitado")
		}
	}
	destino := a.cfg.Caminho("kiwix", nome)
	_ = os.MkdirAll(filepath.Dir(destino), 0o755)
	if err := os.Rename(parcial, destino); err != nil {
		return err
	}
	if err := os.WriteFile(destino+".ok", []byte(resumo+"\n"), 0o644); err != nil {
		return err
	}
	log.Printf("atualização: %s instalado e conferido", nome)
	a.kiwix.Reiniciar()
	return nil
}

func (a *Atualizador) baixarRetomando(ctx context.Context, url, parcial, nome string, feito, total int64) error {
	for tentativa := 0; tentativa < 30 && feito < total; tentativa++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("User-Agent", "offline-library/"+Versao)
		if feito > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", feito))
		}
		resp, err := a.http.Do(req)
		if err != nil {
			time.Sleep(30 * time.Second)
			continue
		}
		if feito > 0 && resp.StatusCode != http.StatusPartialContent {
			resp.Body.Close()
			return fmt.Errorf("servidor não aceitou retomar (%s)", resp.Status)
		}
		if feito == 0 && resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return fmt.Errorf("download: %s", resp.Status)
		}
		f, err := os.OpenFile(parcial, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			resp.Body.Close()
			return err
		}
		buf := make([]byte, 1<<20)
		ultimo := time.Now()
		for {
			n, er := resp.Body.Read(buf)
			if n > 0 {
				if _, ew := f.Write(buf[:n]); ew != nil {
					er = ew
				}
				feito += int64(n)
				if time.Since(ultimo) > 2*time.Second {
					pct := int(feito * 100 / total)
					a.mu.Lock()
					a.baixando[nome] = &andamento{texto: fmt.Sprintf("%s de %s.", tamanhoLegivel(feito), tamanhoLegivel(total)), pct: &pct}
					a.mu.Unlock()
					ultimo = time.Now()
				}
			}
			if er != nil {
				break
			}
		}
		f.Close()
		resp.Body.Close()
	}
	if feito < total {
		return errors.New("download incompleto; continua na próxima rodada")
	}
	if feito > total {
		_ = os.Rename(parcial, parcial+".rejeitado")
		return errors.New("arquivo maior que o oficial; rejeitado")
	}
	return nil
}

func sha256Arquivo(caminho string) (string, error) {
	f, err := os.Open(caminho)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// --- Mapas: geração mais nova do Protomaps, recortada para a América do
// Sul pelo programa pmtiles da plataforma, uma vez por mês.

type estadoMapa struct {
	Geracao string    `json:"geracao"`
	Data    time.Time `json:"data"`
}

func (a *Atualizador) atualizarMapas(ctx context.Context, reg *registroAtualizacao) error {
	if time.Since(reg.UltimaConferencia["mapas"]) < 30*24*time.Hour {
		return nil
	}
	pmtiles, err := a.cfg.Programa("pmtiles")
	if err != nil {
		return nil // sem o programa, mapas ficam como estão
	}
	b, err := a.get(ctx, "https://build-metadata.protomaps.dev/builds.json", 8<<20)
	if err != nil {
		return err
	}
	var builds []struct{ Key, Version string }
	if err := json.Unmarshal(b, &builds); err != nil {
		return err
	}
	hoje := time.Now().UTC().Format("20060102")
	melhor := ""
	for _, x := range builds {
		if regexp.MustCompile(`^\d{8}\.pmtiles$`).MatchString(x.Key) && strings.HasPrefix(x.Version, "4.") && x.Key[:8] <= hoje && x.Key > melhor {
			melhor = x.Key
		}
	}
	if melhor == "" {
		return errors.New("nenhuma geração compatível")
	}
	var atual estadoMapa
	raw, _ := os.ReadFile(a.cfg.Caminho("mapas", "atualizacao.json"))
	_ = json.Unmarshal(raw, &atual)
	if atual.Geracao == "" {
		// Mapa instalado por outra ferramenta: a data do arquivo vale como geração.
		if st, err := os.Stat(a.cfg.Caminho("mapas", "america-sul.pmtiles")); err == nil {
			atual.Geracao = st.ModTime().UTC().Format("20060102")
		}
	}
	if atual.Geracao != "" && strings.TrimSuffix(melhor, ".pmtiles") <= strings.TrimSuffix(atual.Geracao, ".pmtiles") {
		reg.UltimaConferencia["mapas"] = time.Now()
		a.gravarRegistro(*reg)
		return nil
	}
	if livre, _, err := EspacoDisco(a.cfg.Dados); err == nil && int64(livre) < 15*gib {
		return errors.New("espaço insuficiente para o mapa novo")
	}
	// O recorte é feito na pasta de trabalho (disco interno quando há
	// pasta_local) e só depois copiado e relido no disco do acervo: o disco
	// USB já corrompeu gravações sem dar erro.
	trabalho := a.cfg.Trabalho()
	if err := os.MkdirAll(trabalho, 0o755); err != nil {
		return err
	}
	if livre, _, err := EspacoDisco(trabalho); err == nil && int64(livre) < 6*gib {
		return errors.New("espaço insuficiente na pasta de trabalho para o mapa novo")
	}
	gerado := filepath.Join(trabalho, "america-sul.pmtiles.recorte")
	novo := a.cfg.Caminho("mapas", "america-sul.pmtiles.novo")
	_ = os.Remove(novo)
	a.andar("mapas", "Recortando mapa "+melhor+".")
	defer func() { a.mu.Lock(); delete(a.baixando, "mapas"); a.mu.Unlock() }()
	defer os.Remove(gerado)
	cmd := exec.CommandContext(ctx, pmtiles, "extract", "https://build.protomaps.com/"+melhor, gerado,
		"--bbox=-82,-56,-34,13", "--maxzoom=14")
	esconderJanela(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pmtiles extract: %v: %s", err, ultimasLinhas(string(out), 3))
	}
	if st, err := os.Stat(gerado); err != nil || st.Size() < gib {
		return errors.New("mapa novo pequeno demais; descartado")
	}
	if err := verificarPmtiles(ctx, pmtiles, gerado); err != nil {
		return fmt.Errorf("mapa novo recortado com defeito: %v", err)
	}
	if gerado != novo {
		// A falha do disco é intermitente: repetir a cópia sai mais barato
		// que recortar de novo.
		var err error
		for tentativa := 1; tentativa <= 3 && ctx.Err() == nil; tentativa++ {
			a.andar("mapas", fmt.Sprintf("Copiando e conferindo o mapa %s (tentativa %d de 3).", melhor, tentativa))
			if err = copiarConferido(gerado, novo); err == nil {
				err = verificarPmtiles(ctx, pmtiles, novo)
			}
			if err == nil {
				break
			}
			_ = os.Remove(novo)
			log.Printf("atualização: mapas: tentativa %d: %v", tentativa, err)
		}
		if err != nil {
			return fmt.Errorf("cópia do mapa com defeito: %v", err)
		}
	}
	if err := os.Rename(novo, a.cfg.Caminho("mapas", "america-sul.pmtiles")); err != nil {
		return err
	}
	// O resumo antigo descreve o mapa anterior.
	_ = os.Remove(a.cfg.Caminho("mapas", "america-sul.pmtiles.sha256"))
	j, _ := json.Marshal(estadoMapa{Geracao: strings.TrimSuffix(melhor, ".pmtiles"), Data: time.Now()})
	_ = os.WriteFile(a.cfg.Caminho("mapas", "atualizacao.json"), j, 0o644)
	reg.UltimaConferencia["mapas"] = time.Now()
	a.gravarRegistro(*reg)
	log.Printf("atualização: mapa %s instalado", melhor)
	return nil
}

func ultimasLinhas(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " | ")
}

// reporRuntime refaz os programas de apoio desta plataforma quando algum
// falta ou não confere com o resumo gravado, e reinicia o que depende deles.
func (a *Atualizador) reporRuntime(ctx context.Context) error {
	exe := ""
	if runtime.GOOS == "windows" {
		exe = ".exe"
	}
	base := a.cfg.PastaRuntime(Plataforma())
	motivo := ""
	for _, p := range []string{"kiwix-serve" + exe, "pmtiles" + exe, filepath.Join("jre", "bin", "java"+exe)} {
		if _, err := os.Stat(filepath.Join(base, p)); err != nil {
			motivo = p + " ausente"
		}
	}
	if motivo == "" {
		if err := ConferirRuntime(a.cfg, Plataforma()); err != nil {
			motivo = err.Error()
		}
	}
	if motivo == "" {
		return nil
	}
	log.Printf("atualização: programas de apoio de %s: %s; refazendo", Plataforma(), motivo)
	a.andar("runtime", "Refazendo programas de apoio para "+Plataforma()+".")
	defer func() { a.mu.Lock(); delete(a.baixando, "runtime"); a.mu.Unlock() }()
	if err := PrepararRuntime(ctx, a.cfg, []string{Plataforma()}); err != nil {
		return err
	}
	a.kiwix.Reiniciar()
	if a.rotas != nil {
		a.rotas.Reiniciar()
	}
	return nil
}

// verificarPmtiles confere a estrutura do arquivo (cabeçalho, diretórios e
// limites) sem abrir cada trecho do mapa.
func verificarPmtiles(ctx context.Context, pmtiles, caminho string) error {
	esquecerCache(caminho)
	cmd := exec.CommandContext(ctx, pmtiles, "verify", caminho)
	esconderJanela(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, ultimasLinhas(string(out), 3))
	}
	return nil
}
