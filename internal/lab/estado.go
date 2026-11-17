package lab

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Zim é um pacote da enciclopédia instalado.
type Zim struct {
	Arquivo string
	Caminho string
	Prefixo string // wikipedia_pt_all_maxi_
	Edicao  string // 2026-05
	Tamanho int64
}

var reZim = regexp.MustCompile(`^(.+_)(\d{4}-\d{2})\.zim$`)

func analisarZim(nome string) (prefixo, edicao string, ok bool) {
	m := reZim.FindStringSubmatch(nome)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// ZimsProntos lista os ZIM com marcador .ok, um por coleção (a edição
// mais nova).
func ZimsProntos(c *Config) []Zim {
	entradas, _ := os.ReadDir(c.Caminho("kiwix"))
	porPrefixo := map[string]Zim{}
	for _, e := range entradas {
		n := e.Name()
		if strings.HasPrefix(n, "._") || !strings.HasSuffix(n, ".zim") {
			continue
		}
		caminho := c.Caminho("kiwix", n)
		if _, err := os.Stat(caminho + ".ok"); err != nil {
			continue
		}
		pre, ed, ok := analisarZim(n)
		if !ok {
			pre, ed = n, ""
		}
		info, _ := e.Info()
		z := Zim{Arquivo: n, Caminho: caminho, Prefixo: pre, Edicao: ed}
		if info != nil {
			z.Tamanho = info.Size()
		}
		if atual, ok := porPrefixo[pre]; !ok || z.Edicao > atual.Edicao {
			porPrefixo[pre] = z
		}
	}
	var lista []Zim
	for _, z := range porPrefixo {
		lista = append(lista, z)
	}
	sort.Slice(lista, func(i, j int) bool { return lista[i].Arquivo < lista[j].Arquivo })
	return lista
}

var idiomas = map[string]string{"pt": "português", "en": "inglês", "es": "espanhol"}

// TituloColecao transforma wikipedia_pt_all_maxi_ em "Wikipédia em português".
func TituloColecao(prefixo string) string {
	partes := strings.Split(strings.TrimSuffix(prefixo, "_"), "_")
	if len(partes) < 2 {
		return prefixo
	}
	projeto := map[string]string{"wikipedia": "Wikipédia", "wiktionary": "Wikcionário",
		"wikibooks": "Wikilivros", "wikivoyage": "Wikivoyage"}[partes[0]]
	if projeto == "" {
		projeto = partes[0]
	}
	idioma := idiomas[partes[1]]
	if idioma == "" {
		idioma = partes[1]
	}
	return projeto + " em " + idioma
}

// Acervo é a lista desejada em config/acervo.txt: "categoria arquivo.zim".
type ItemAcervo struct{ Categoria, Arquivo, Prefixo string }

func LerAcervo(c *Config) []ItemAcervo {
	f, err := os.Open(c.Caminho("config", "acervo.txt"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var lista []ItemAcervo
	s := bufio.NewScanner(f)
	for s.Scan() {
		campos := strings.Fields(s.Text())
		if len(campos) != 2 || strings.HasPrefix(campos[0], "#") {
			continue
		}
		pre, _, ok := analisarZim(campos[1])
		if !ok {
			continue
		}
		lista = append(lista, ItemAcervo{campos[0], campos[1], pre})
	}
	return lista
}

func tamanhoLegivel(b int64) string {
	switch {
	case b >= 1<<30:
		return strings.Replace(fmt.Sprintf("%.1f GB", float64(b)/(1<<30)), ".", ",", 1)
	case b >= 1<<20:
		return fmt.Sprintf("%d MB", b/(1<<20))
	default:
		return fmt.Sprintf("%d KB", b/1024)
	}
}

type parte struct {
	Estado  string `json:"estado"`
	Detalhe string `json:"detalhe"`
}

type colecao struct {
	Titulo  string `json:"titulo"`
	Estado  string `json:"estado"`
	Detalhe string `json:"detalhe"`
	Edicao  string `json:"edicao,omitempty"`
	Pct     *int   `json:"pct,omitempty"`
}

// estado responde do cache por alguns segundos: cada cálculo lê o disco,
// que em USB lento pode travar por dezenas de segundos.
func (s *Servidor) estado(w http.ResponseWriter, r *http.Request) {
	s.estadoMu.Lock()
	if time.Since(s.estadoEm) > 10*time.Second || s.estadoCache == nil {
		s.estadoCache = s.calcularEstado()
		s.estadoEm = time.Now()
	}
	corpo := s.estadoCache
	s.estadoMu.Unlock()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(corpo)
}

func (s *Servidor) calcularEstado() []byte {
	c := s.cfg
	resp := map[string]any{"nome": c.Nome, "atualizado": time.Now().UTC().Format(time.RFC3339),
		"plataforma": Plataforma(), "versao": Versao}
	instalados := ZimsProntos(c)
	var cols []colecao
	vistos := map[string]bool{}
	for _, z := range instalados {
		vistos[z.Prefixo] = true
		cols = append(cols, colecao{Titulo: TituloColecao(z.Prefixo), Estado: "pronto",
			Detalhe: tamanhoLegivel(z.Tamanho), Edicao: z.Edicao})
	}
	for _, a := range LerAcervo(c) {
		if vistos[a.Prefixo] {
			continue
		}
		vistos[a.Prefixo] = true
		col := colecao{Titulo: TituloColecao(a.Prefixo), Estado: "fila", Detalhe: "Na fila de download."}
		if d := s.atualizador.progresso(a.Arquivo); d != nil {
			col.Estado, col.Detalhe, col.Pct = "baixando", d.texto, d.pct
		} else if st, err := os.Stat(c.Caminho("downloads", a.Arquivo)); err == nil {
			col.Estado, col.Detalhe = "parado", "Download parcial de "+tamanhoLegivel(st.Size())+"."
		}
		cols = append(cols, col)
	}
	resp["colecoes"] = cols

	enc, det := s.kiwix.Estado()
	resp["enciclopedia"] = parte{enc, det}
	rot, rdet := s.rotas.Estado()
	resp["rotas"] = parte{rot, rdet}
	mapa := parte{"pendente", "Mapa ainda não instalado."}
	for _, n := range []string{"america-sul.pmtiles", "curitiba.pmtiles", "panorama.pmtiles"} {
		if st, err := os.Stat(c.Caminho("mapas", n)); err == nil {
			mapa = parte{"pronto", "Mapa " + strings.TrimSuffix(n, ".pmtiles") + ", " + tamanhoLegivel(st.Size()) + "."}
			break
		}
	}
	if rot == "pronto" {
		mapa.Detalhe += " Caminhos disponíveis."
	}
	if s.busca.pronta() {
		mapa.Detalhe += " Busca por endereço disponível."
	}
	resp["mapas"] = mapa
	resp["catalogo"] = s.catalogo.estado()
	if livre, total, err := EspacoDisco(c.Dados); err == nil {
		resp["disco"] = map[string]string{"livre": tamanhoLegivel(int64(livre)), "total": tamanhoLegivel(int64(total))}
	}
	resp["atualizacoes"] = s.atualizador.resumo()
	b, _ := json.Marshal(resp)
	return b
}

// Verificar confere cada serviço por dentro e devolve o resultado.
func (s *Servidor) verificar(w http.ResponseWriter, r *http.Request) {
	type item struct{ Nome, Resultado, Detalhe string }
	var itens []item
	add := func(n string, ok bool, d string) {
		res := "passou"
		if !ok {
			res = "falhou"
		}
		itens = append(itens, item{n, res, d})
	}
	e, d := s.kiwix.Estado()
	add("Enciclopédia", e == "pronto", d)
	e, d = s.rotas.Estado()
	add("Rotas", e == "pronto", d)
	add("Catálogo", s.catalogo.db != nil, "Banco SQLite em "+filepath.Join("catalogo-lab", "catalogo.db"))
	add("Busca de lugares", s.busca.pronta(), "Índice em mapas/busca.db")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(itens)
}
