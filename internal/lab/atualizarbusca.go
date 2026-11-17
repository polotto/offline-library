package lab

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// OSMPadrao é o recorte da América do Sul do Geofabrik. O endereço
// "latest" redireciona para o arquivo datado do dia.
const OSMPadrao = "https://download.geofabrik.de/south-america-latest.osm.pbf"

type estadoBusca struct {
	Versao string    `json:"versao"`
	Fonte  string    `json:"fonte"`
	Data   time.Time `json:"data"`
}

func (a *Atualizador) andar(chave, texto string) {
	a.mu.Lock()
	a.baixando[chave] = &andamento{texto: texto}
	a.mu.Unlock()
	log.Printf("atualização: %s", texto)
}

// AtualizarBusca roda só a etapa do índice de endereços. Com forcar,
// ignora o intervalo mensal (comando "lab atualizar-busca").
func (a *Atualizador) AtualizarBusca(ctx context.Context, forcar bool) error {
	reg := a.registro()
	if forcar {
		delete(reg.UltimaConferencia, "busca")
	}
	return a.atualizarBusca(ctx, &reg)
}

// atualizarBusca baixa o OpenStreetMap com retomada, confere o MD5
// oficial, gera o índice na pasta de trabalho, copia para mapas/ e
// confere a cópia. Só troca o índice em uso quando tudo passa.
func (a *Atualizador) atualizarBusca(ctx context.Context, reg *registroAtualizacao) error {
	destino := a.cfg.Caminho("mapas", "busca.db")
	_, semIndice := os.Stat(destino)
	if semIndice == nil && time.Since(reg.UltimaConferencia["busca"]) < 30*24*time.Hour {
		return nil
	}
	defer func() { a.mu.Lock(); delete(a.baixando, "busca"); a.mu.Unlock() }()

	fonte := a.cfg.OSM
	if fonte == "" {
		fonte = OSMPadrao
	}
	url, tamanho, versao, err := a.resolverArquivo(ctx, fonte)
	if err != nil {
		return err
	}
	var atual estadoBusca
	raw, _ := os.ReadFile(a.cfg.Caminho("mapas", "busca.json"))
	_ = json.Unmarshal(raw, &atual)
	if semIndice == nil && atual.Versao == versao {
		log.Printf("atualização: busca de endereços já está na versão %s", versao)
		a.marcarConferido(reg, "busca")
		return nil
	}
	oficial := ""
	if b, err := a.get(ctx, url+".md5", 4096); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 && len(f[0]) == 32 {
			oficial = strings.ToLower(f[0])
		}
	}
	if oficial == "" {
		return errors.New("resumo MD5 oficial do OpenStreetMap ausente")
	}

	trabalho := a.cfg.PastaTrabalho
	if trabalho == "" {
		trabalho = a.cfg.Caminho("downloads", "busca")
	}
	if err := os.MkdirAll(trabalho, 0o755); err != nil {
		return err
	}
	limparOutrasVersoes(trabalho, filepath.Base(url))
	pbf := filepath.Join(trabalho, filepath.Base(url))
	if _, err := os.Stat(pbf); err != nil {
		parcial := pbf + ".parcial"
		var feito int64
		if st, err := os.Stat(parcial); err == nil {
			feito = st.Size()
		}
		// Arquivo, temporários da geração e o índice: cerca de 2,5 vezes o pbf.
		if livre, _, err := EspacoDisco(trabalho); err == nil && int64(livre) < tamanho-feito+tamanho*3/2 {
			return fmt.Errorf("espaço insuficiente em %s para gerar a busca", trabalho)
		}
		if feito < tamanho {
			a.andar("busca", "Baixando o OpenStreetMap para a busca de endereços.")
			if err := a.baixarRetomando(ctx, url, parcial, "busca", feito, tamanho); err != nil {
				return err
			}
		}
		a.andar("busca", "Conferindo o resumo MD5 oficial do OpenStreetMap.")
		soma, err := md5Arquivo(parcial)
		if err != nil {
			return err
		}
		if soma != oficial {
			_ = os.Remove(parcial)
			return errors.New("OpenStreetMap com resumo MD5 diferente do oficial; apagado, baixa de novo na próxima rodada")
		}
		if err := os.Rename(parcial, pbf); err != nil {
			return err
		}
	}

	a.andar("busca", "Gerando o índice de endereços. Pode levar algumas horas.")
	if os.Getenv("GOMEMLIMIT") == "" {
		anterior := debug.SetMemoryLimit(900 << 20)
		defer debug.SetMemoryLimit(anterior)
	}
	gerado := filepath.Join(trabalho, "busca.db")
	if err := GerarBusca(ctx, pbf, gerado); err != nil {
		return fmt.Errorf("gerar busca: %w", err)
	}
	a.andar("busca", "Copiando e conferindo o índice de endereços.")
	novo := destino + ".novo"
	if err := copiarConferido(gerado, novo); err != nil {
		_ = os.Remove(novo)
		return err
	}
	if a.antesDeTrocarBusca != nil {
		a.antesDeTrocarBusca()
	}
	if err := os.Rename(novo, destino); err != nil {
		return err
	}
	j, _ := json.Marshal(estadoBusca{Versao: versao, Fonte: url, Data: time.Now()})
	_ = os.WriteFile(a.cfg.Caminho("mapas", "busca.json"), j, 0o644)
	_ = os.Remove(gerado)
	_ = os.Remove(pbf)
	a.marcarConferido(reg, "busca")
	log.Printf("atualização: busca de endereços %s instalada", versao)
	return nil
}

// RegistrarBusca grava a versão de um índice gerado à mão, para o
// atualizador não refazer o mesmo trabalho.
func RegistrarBusca(cfg *Config, versao, fonte string) error {
	j, _ := json.Marshal(estadoBusca{Versao: versao, Fonte: fonte, Data: time.Now()})
	return os.WriteFile(cfg.Caminho("mapas", "busca.json"), j, 0o644)
}

func (a *Atualizador) marcarConferido(reg *registroAtualizacao, chave string) {
	reg.UltimaConferencia[chave] = time.Now()
	a.gravarRegistro(*reg)
}

// resolverArquivo segue redirecionamentos e devolve o endereço final, o
// tamanho e uma versão: o nome datado do arquivo e a data de modificação.
func (a *Atualizador) resolverArquivo(ctx context.Context, url string) (string, int64, string, error) {
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(c, http.MethodHead, url, nil)
	req.Header.Set("User-Agent", "offline-library/"+Versao)
	resp, err := a.http.Do(req)
	if err != nil {
		return "", 0, "", err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength <= 0 {
		return "", 0, "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	final := resp.Request.URL.String()
	versao := strings.TrimSuffix(filepath.Base(resp.Request.URL.Path), ".osm.pbf")
	if final == url {
		// Sem redirecionamento para arquivo datado: a data do servidor vale.
		versao += " " + resp.Header.Get("Last-Modified")
	}
	return final, resp.ContentLength, versao, nil
}

func limparOutrasVersoes(pasta, manter string) {
	entradas, _ := os.ReadDir(pasta)
	for _, e := range entradas {
		n := e.Name()
		if strings.HasSuffix(n, ".osm.pbf") || strings.HasSuffix(n, ".osm.pbf.parcial") {
			if !strings.HasPrefix(n, manter) {
				_ = os.Remove(filepath.Join(pasta, n))
			}
		}
		if strings.HasPrefix(n, "busca-tmp-") || n == "busca.db.gerando" {
			_ = os.RemoveAll(filepath.Join(pasta, n))
		}
	}
}

func md5Arquivo(caminho string) (string, error) {
	f, err := os.Open(caminho)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copiarConferido copia e relê a cópia: disco USB instável já corrompeu
// arquivos sem dar erro na escrita.
func copiarConferido(origem, destino string) error {
	o, err := os.Open(origem)
	if err != nil {
		return err
	}
	defer o.Close()
	d, err := os.Create(destino)
	if err != nil {
		return err
	}
	if _, err := io.Copy(d, o); err != nil {
		d.Close()
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	if err := d.Close(); err != nil {
		return err
	}
	a, err := sha256Arquivo(origem)
	if err != nil {
		return err
	}
	b, err := sha256Arquivo(destino)
	if err != nil {
		return err
	}
	if a != b {
		return errors.New("cópia do índice diferente da origem; disco instável")
	}
	return nil
}
