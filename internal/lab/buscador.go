package lab

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/html"
)

const esquemaConteudoLivros = `
CREATE TABLE IF NOT EXISTS arquivos_indexados (
  arquivo_id INTEGER PRIMARY KEY,
  tamanho INTEGER NOT NULL,
  trechos INTEGER NOT NULL
);
CREATE VIRTUAL TABLE IF NOT EXISTS livros_fts USING fts5(
  item_id UNINDEXED,
  arquivo_id UNINDEXED,
  local UNINDEXED,
  texto,
  tokenize = 'unicode61 remove_diacritics 2'
);
`

type trechoLivro struct {
	Local string
	Texto string
}

type ResultadoWiki struct {
	Titulo  string `json:"titulo"`
	Colecao string `json:"colecao"`
	Trecho  string `json:"trecho"`
	URL     string `json:"url"`
}

type ResultadoLivro struct {
	ItemID     int64  `json:"item_id"`
	ArquivoID  int64  `json:"arquivo_id,omitempty"`
	Titulo     string `json:"titulo"`
	Autores    string `json:"autores,omitempty"`
	Local      string `json:"local"`
	Trecho     string `json:"trecho"`
	URL        string `json:"url"`
	URLLeitura string `json:"url_leitura,omitempty"`
}

type ResultadoEndereco struct {
	Nome    string  `json:"nome"`
	Detalhe string  `json:"detalhe"`
	Tipo    string  `json:"tipo"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	URL     string  `json:"url"`
}

type RespostaBuscaUnificada struct {
	Consulta        string              `json:"consulta"`
	Wiki            []ResultadoWiki     `json:"wiki"`
	Livros          []ResultadoLivro    `json:"livros"`
	Enderecos       []ResultadoEndereco `json:"enderecos"`
	IndexandoLivros bool                `json:"indexando_livros,omitempty"`
}

// handlerBuscaUnificada consulta enciclopédia (Kiwix), endereços (OSM) e
// catálogo + conteúdo interno dos livros em paralelo.
func (s *Servidor) handlerBuscaUnificada(w http.ResponseWriter, r *http.Request) {
	termo := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	resp := RespostaBuscaUnificada{
		Consulta:        termo,
		Wiki:            []ResultadoWiki{},
		Livros:          []ResultadoLivro{},
		Enderecos:       []ResultadoEndereco{},
		IndexandoLivros: s.catalogo.indexando.Load(),
	}
	if utf8.RuneCountInString(termo) < 2 {
		_ = json.NewEncoder(w).Encode(resp)
		return
	}

	// fonte=wiki|livros|enderecos deixa a página pedir cada parte em
	// separado e mostrar a mais rápida primeiro. Sem fonte, busca tudo.
	fonte := r.URL.Query().Get("fonte")
	quer := func(nome string) bool { return fonte == "" || fonte == nome }
	ctx, cancel := context.WithTimeout(r.Context(), 16*time.Second)
	defer cancel()
	logado := s.catalogo.usuario(r) != ""

	chaveCache := semAcento(termo)
	var wg sync.WaitGroup
	if quer("wiki") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, ok := cacheBuscador.pegar("wiki|" + chaveCache); ok {
				resp.Wiki = v.([]ResultadoWiki)
				return
			}
			ctxWiki, cancelWiki := context.WithTimeout(ctx, 15*time.Second)
			defer cancelWiki()
			res, completo := s.buscarWiki(ctxWiki, termo, 8)
			if res == nil {
				res = []ResultadoWiki{}
			}
			if completo {
				cacheBuscador.guardar("wiki|"+chaveCache, res)
			}
			resp.Wiki = res
		}()
	}
	if quer("livros") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if res := s.catalogo.BuscarLivros(termo, logado, 8); len(res) > 0 {
				resp.Livros = res
			}
		}()
	}
	if quer("enderecos") {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if v, ok := cacheBuscador.pegar("enderecos|" + chaveCache); ok {
				resp.Enderecos = v.([]ResultadoEndereco)
				return
			}
			ctxEnd, cancelEnd := context.WithTimeout(ctx, 3*time.Second)
			defer cancelEnd()
			if res := s.buscarEnderecosRapido(ctxEnd, termo, 6); len(res) > 0 {
				resp.Enderecos = res
				cacheBuscador.guardar("enderecos|"+chaveCache, res)
			}
		}()
	}
	wg.Wait()

	_ = json.NewEncoder(w).Encode(resp)
}

// --- Busca na enciclopédia e dicionários (Kiwix)

type sugestaoKiwix struct {
	Value string `json:"value"`
	Kind  string `json:"kind"`
	Path  string `json:"path"`
}

func montarURLArtigoWiki(acervo, caminho string) string {
	segs := strings.Split(strings.TrimPrefix(caminho, "/"), "/")
	var limpos []string
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			continue
		}
		limpos = append(limpos, url.PathEscape(s))
	}
	if len(limpos) == 0 {
		return ""
	}
	return "/wiki/content/" + url.PathEscape(acervo) + "/" + strings.Join(limpos, "/") + "?leitura=1"
}

func (s *Servidor) sugerirKiwix(ctx context.Context, acervo, colecao, termo string, max int) []ResultadoWiki {
	q := url.Values{"content": {acervo}, "term": {termo}, "userlang": {"pt"}}
	alvo := fmt.Sprintf("http://127.0.0.1:%d/wiki/suggest?%s", s.kiwix.Porta, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, alvo, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var sugs []sugestaoKiwix
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&sugs); err != nil {
		return nil
	}
	var out []ResultadoWiki
	for _, d := range sugs {
		if d.Kind != "path" || d.Path == "" {
			continue
		}
		u := montarURLArtigoWiki(acervo, d.Path)
		if u == "" {
			continue
		}
		out = append(out, ResultadoWiki{
			Titulo:  strings.TrimSpace(d.Value),
			Colecao: colecao,
			URL:     u,
		})
		if len(out) >= max {
			break
		}
	}
	return out
}

func (s *Servidor) pesquisarKiwixHTML(ctx context.Context, acervo, colecao, termo string, max int) []ResultadoWiki {
	q := url.Values{
		"content":    {acervo},
		"pattern":    {termo},
		"pageLength": {strconv.Itoa(max)},
		"userlang":   {"pt"},
	}
	alvo := fmt.Sprintf("http://127.0.0.1:%d/wiki/search?%s", s.kiwix.Porta, q.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, alvo, nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	return extrairResultadosKiwixHTML(io.LimitReader(resp.Body, 512<<10), colecao, max)
}

func extrairResultadosKiwixHTML(r io.Reader, colecao string, max int) []ResultadoWiki {
	doc, err := html.Parse(r)
	if err != nil {
		return nil
	}
	var resultados []ResultadoWiki
	var percorrer func(*html.Node)
	percorrer = func(n *html.Node) {
		if len(resultados) >= max {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" {
			for _, a := range n.Attr {
				if a.Key == "class" && strings.Contains(a.Val, "results") {
					coletarItensKiwix(n, colecao, max, &resultados)
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			percorrer(c)
		}
	}
	percorrer(doc)
	return resultados
}

func coletarItensKiwix(n *html.Node, colecao string, max int, out *[]ResultadoWiki) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if len(*out) >= max {
			return
		}
		if c.Type == html.ElementNode && c.Data == "li" {
			var titulo, href, trecho string
			for f := c.FirstChild; f != nil; f = f.NextSibling {
				if f.Type != html.ElementNode {
					continue
				}
				switch f.Data {
				case "a":
					if href == "" {
						for _, a := range f.Attr {
							if a.Key == "href" {
								href = strings.TrimSpace(a.Val)
							}
						}
						titulo = limparEspacos(textoPlanoHTML(f))
					}
				case "cite":
					trecho = limparEspacos(textoPlanoHTML(f))
				}
			}
			if titulo != "" && strings.HasPrefix(href, "/wiki/content/") {
				if !strings.Contains(href, "?") {
					href += "?leitura=1"
				}
				*out = append(*out, ResultadoWiki{
					Titulo:  titulo,
					Colecao: colecao,
					Trecho:  trecho,
					URL:     href,
				})
			}
			continue
		}
		coletarItensKiwix(c, colecao, max, out)
	}
}

// --- Indexação e busca do conteúdo interno dos livros
//
// O texto de dentro dos livros fica num SQLite próprio (livros.db), fora do
// catalogo.db: é dado derivado, refeito sozinho se faltar ou corromper, e vai
// para a pasta_local quando ela existe (disco do acervo instável para gravar).

// abrirIndiceLivros abre o índice e confere a integridade. Se estiver
// corrompido, apaga e recria vazio; a próxima rodada reindexa tudo.
func abrirIndiceLivros(caminho string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(caminho), 0o755); err != nil {
		return nil, err
	}
	abrir := func() (*sql.DB, error) {
		db, err := abrirSQLite(caminho, false)
		if err != nil {
			return nil, err
		}
		db.SetMaxOpenConns(1)
		var ok string
		if err := db.QueryRow(`PRAGMA quick_check`).Scan(&ok); err != nil || ok != "ok" {
			db.Close()
			return nil, fmt.Errorf("índice dos livros corrompido: %v %s", err, ok)
		}
		if _, err := db.Exec(esquemaConteudoLivros); err != nil {
			db.Close()
			return nil, err
		}
		return db, nil
	}
	db, err := abrir()
	if err == nil {
		return db, nil
	}
	log.Printf("catálogo: %v; refazendo %s", err, caminho)
	for _, sufixo := range []string{"", "-journal"} {
		_ = os.Remove(caminho + sufixo)
	}
	return abrir()
}

// IniciarIndexacaoConteudo indexa em segundo plano os livros novos ou
// alterados. Com ctx nil, usa o contexto guardado na primeira chamada.
func (c *Catalogo) IniciarIndexacaoConteudo(ctx context.Context) {
	if c.idx == nil {
		return
	}
	if ctx == nil {
		ctx = c.ctxIndice
	} else if c.ctxIndice == nil {
		c.ctxIndice = ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		if err := c.IndexarConteudoPendente(ctx); err != nil && ctx.Err() == nil {
			log.Printf("catálogo: indexação de conteúdo: %v", err)
		}
	}()
}

func prioridadeFormatoLivro(nome string) int {
	switch strings.ToLower(filepath.Ext(nome)) {
	case ".pdf":
		return 5
	case ".epub":
		return 4
	case ".azw3":
		return 3
	case ".mobi":
		return 2
	case ".txt", ".md":
		return 1
	default:
		return 0
	}
}

// IndexarConteudoPendente extrai e indexa o texto dos livros que ainda não
// estão no índice ou mudaram de tamanho. Escolhe um formato por item (PDF,
// depois EPUB, AZW3, MOBI, TXT) para não repetir o mesmo livro.
func (c *Catalogo) IndexarConteudoPendente(ctx context.Context) error {
	if c.idx == nil {
		return nil
	}
	c.indexMu.Lock()
	defer c.indexMu.Unlock()

	rows, err := c.db.QueryContext(ctx, `SELECT id, item_id, nome, caminho, tamanho FROM arquivos ORDER BY item_id, id`)
	if err != nil {
		return err
	}
	type arqInfo struct {
		id, itemID, tamanho int64
		nome, caminho       string
		prio                int
	}
	porItem := map[int64]arqInfo{}
	existe := map[int64]bool{}
	for rows.Next() {
		var a arqInfo
		if err := rows.Scan(&a.id, &a.itemID, &a.nome, &a.caminho, &a.tamanho); err != nil {
			rows.Close()
			return err
		}
		existe[a.id] = true
		a.prio = prioridadeFormatoLivro(a.nome)
		if a.prio == 0 {
			continue
		}
		if atual, ok := porItem[a.itemID]; !ok || a.prio > atual.prio {
			porItem[a.itemID] = a
		}
	}
	rows.Close()
	escolhido := map[int64]bool{}
	for _, a := range porItem {
		escolhido[a.id] = true
	}

	indexados := map[int64]int64{}
	idxRows, err := c.idx.QueryContext(ctx, `SELECT arquivo_id, tamanho FROM arquivos_indexados`)
	if err != nil {
		return err
	}
	for idxRows.Next() {
		var aid, tam int64
		if idxRows.Scan(&aid, &tam) == nil {
			indexados[aid] = tam
		}
	}
	idxRows.Close()

	// Sai do índice o arquivo apagado ou trocado por formato melhor no item.
	for aid := range indexados {
		if !existe[aid] || !escolhido[aid] {
			if err := c.removerDoIndice(ctx, aid); err != nil {
				return err
			}
			delete(indexados, aid)
		}
	}

	var pendentes []arqInfo
	for _, a := range porItem {
		if tam, ok := indexados[a.id]; !ok || tam != a.tamanho {
			pendentes = append(pendentes, a)
		}
	}
	if len(pendentes) == 0 {
		return nil
	}
	sort.Slice(pendentes, func(i, j int) bool { return pendentes[i].itemID < pendentes[j].itemID })

	c.indexando.Store(true)
	defer c.indexando.Store(false)
	inicio := time.Now()
	for _, a := range pendentes {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		caminhoAbs := filepath.Join(c.dir, "arquivos", filepath.FromSlash(a.caminho))
		trechos, err := extrairTrechosLivro(ctx, caminhoAbs, a.nome)
		if err != nil {
			// Marca como lido para não tentar de novo a cada rodada; só volta
			// se o arquivo mudar de tamanho.
			log.Printf("catálogo: sem texto em %s: %v", a.nome, err)
			trechos = nil
		}
		if err := c.gravarTrechosArquivo(ctx, a.id, a.itemID, a.tamanho, trechos); err != nil {
			return err
		}
	}
	log.Printf("catálogo: %d livros indexados em %s", len(pendentes), time.Since(inicio).Round(time.Second))
	return nil
}

func (c *Catalogo) removerDoIndice(ctx context.Context, arquivoID int64) error {
	if _, err := c.idx.ExecContext(ctx, `DELETE FROM livros_fts WHERE arquivo_id = ?`, arquivoID); err != nil {
		return err
	}
	_, err := c.idx.ExecContext(ctx, `DELETE FROM arquivos_indexados WHERE arquivo_id = ?`, arquivoID)
	return err
}

func (c *Catalogo) gravarTrechosArquivo(ctx context.Context, arquivoID, itemID, tamanho int64, trechos []trechoLivro) error {
	tx, err := c.idx.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM livros_fts WHERE arquivo_id = ?`, arquivoID); err != nil {
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO livros_fts(item_id, arquivo_id, local, texto) VALUES(?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	n := 0
	for _, t := range trechos {
		if strings.TrimSpace(t.Texto) == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, itemID, arquivoID, t.Local, t.Texto); err != nil {
			return err
		}
		n++
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO arquivos_indexados(arquivo_id, tamanho, trechos) VALUES(?,?,?)
		ON CONFLICT(arquivo_id) DO UPDATE SET tamanho=excluded.tamanho, trechos=excluded.trechos`,
		arquivoID, tamanho, n); err != nil {
		return err
	}
	return tx.Commit()
}

func urlLeituraDe(arquivoID int64, nomeArquivo, local string) string {
	ext := strings.ToLower(filepath.Ext(nomeArquivo))
	if ext != ".pdf" && ext != ".epub" {
		return ""
	}
	base := fmt.Sprintf("/catalogo/ler/%d", arquivoID)
	if ext == ".pdf" && strings.HasPrefix(local, "Página ") {
		num := strings.TrimSpace(strings.TrimPrefix(local, "Página "))
		if _, err := strconv.Atoi(num); err == nil {
			return base + "?pagina=" + num
		}
	}
	return base
}

type fichaLivro struct {
	titulo, autores, arquivo string
	publico                  bool
}

// fichaDe lê título, autores e visibilidade no catálogo. ok=false se o item
// ou o arquivo não existem mais.
func (c *Catalogo) fichaDe(itemID, arquivoID int64) (fichaLivro, bool) {
	var f fichaLivro
	err := c.db.QueryRow(`SELECT i.titulo, i.autores, i.publico, a.nome
		FROM itens i JOIN arquivos a ON a.item_id = i.id
		WHERE i.id = ? AND a.id = ?`, itemID, arquivoID).Scan(&f.titulo, &f.autores, &f.publico, &f.arquivo)
	return f, err == nil
}

// BuscarLivros procura nos metadados do catálogo (título, autor, assunto, tags)
// e no texto de dentro dos livros (PDF, EPUB, MOBI, AZW3, TXT).
func (c *Catalogo) BuscarLivros(termo string, logado bool, limite int) []ResultadoLivro {
	q := consultaFTS(termo)
	if q == "" || limite <= 0 {
		return nil
	}
	palavrasBusca := strings.FieldsFunc(semAcento(termo), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})

	var resultados []ResultadoLivro
	porItem := map[int64]int{}

	// 1. Ficha catalográfica (itens_fts).
	filtroPublico := ""
	if !logado {
		filtroPublico = " AND i.publico = 1"
	}
	rowsMeta, err := c.db.Query(`
		SELECT i.id, i.titulo, i.autores, i.assunto, i.descricao,
		       COALESCE((SELECT a.id FROM arquivos a WHERE a.item_id = i.id AND (LOWER(a.nome) LIKE '%.pdf' OR LOWER(a.nome) LIKE '%.epub') ORDER BY CASE WHEN LOWER(a.nome) LIKE '%.pdf' THEN 0 ELSE 1 END, a.id LIMIT 1), 0),
		       COALESCE((SELECT a.nome FROM arquivos a WHERE a.item_id = i.id AND (LOWER(a.nome) LIKE '%.pdf' OR LOWER(a.nome) LIKE '%.epub') ORDER BY CASE WHEN LOWER(a.nome) LIKE '%.pdf' THEN 0 ELSE 1 END, a.id LIMIT 1), '')
		FROM itens_fts
		JOIN itens i ON i.id = itens_fts.rowid
		WHERE itens_fts MATCH ?`+filtroPublico+`
		ORDER BY itens_fts.rank LIMIT ?`, q, limite)
	if err == nil {
		for rowsMeta.Next() {
			var id, arqID int64
			var titulo, autores, assunto, desc, arqNome string
			if rowsMeta.Scan(&id, &titulo, &autores, &assunto, &desc, &arqID, &arqNome) != nil {
				continue
			}
			resumo := strings.TrimSpace(desc)
			if resumo == "" {
				resumo = strings.TrimSpace(assunto)
			}
			if resumo != "" {
				resumo = recortarTrecho(resumo, palavrasBusca, 180)
			}
			resultados = append(resultados, ResultadoLivro{
				ItemID:     id,
				ArquivoID:  arqID,
				Titulo:     titulo,
				Autores:    strings.ReplaceAll(autores, "\n", ", "),
				Local:      "Ficha do livro",
				Trecho:     resumo,
				URL:        fmt.Sprintf("/catalogo/item/%d", id),
				URLLeitura: urlLeituraDe(arqID, arqNome, ""),
			})
			porItem[id]++
		}
		rowsMeta.Close()
	}
	if c.idx == nil {
		return resultados
	}

	// 2. Texto de dentro das páginas e capítulos (livros.db), melhores primeiro.
	type achado struct {
		itemID, arqID int64
		local, texto  string
	}
	var achados []achado
	rowsTxt, err := c.idx.Query(`
		SELECT CAST(item_id AS INTEGER), CAST(arquivo_id AS INTEGER), local, texto
		FROM livros_fts WHERE livros_fts MATCH ? ORDER BY rank LIMIT 80`, q)
	if err == nil {
		for rowsTxt.Next() {
			var a achado
			if rowsTxt.Scan(&a.itemID, &a.arqID, &a.local, &a.texto) == nil {
				achados = append(achados, a)
			}
		}
		rowsTxt.Close()
	}
	fichas := map[int64]*fichaLivro{}
	for _, a := range achados {
		if len(resultados) >= limite {
			break
		}
		// Até 2 trechos por livro, para caber mais livros na lista.
		if porItem[a.itemID] >= 2 {
			continue
		}
		f, visto := fichas[a.arqID]
		if !visto {
			if ficha, ok := c.fichaDe(a.itemID, a.arqID); ok {
				f = &ficha
			}
			fichas[a.arqID] = f
		}
		if f == nil || (!logado && !f.publico) {
			continue
		}
		trecho := recortarTrecho(a.texto, palavrasBusca, 190)
		enriquecido := false
		for k := range resultados {
			if resultados[k].ItemID == a.itemID && resultados[k].Local == "Ficha do livro" && resultados[k].Trecho == "" {
				resultados[k].Local, resultados[k].Trecho = a.local, trecho
				if u := urlLeituraDe(a.arqID, f.arquivo, a.local); u != "" {
					resultados[k].ArquivoID, resultados[k].URLLeitura = a.arqID, u
				}
				enriquecido = true
				break
			}
		}
		if enriquecido {
			continue
		}
		resultados = append(resultados, ResultadoLivro{
			ItemID:     a.itemID,
			ArquivoID:  a.arqID,
			Titulo:     f.titulo,
			Autores:    strings.ReplaceAll(f.autores, "\n", ", "),
			Local:      a.local,
			Trecho:     trecho,
			URL:        fmt.Sprintf("/catalogo/item/%d", a.itemID),
			URLLeitura: urlLeituraDe(a.arqID, f.arquivo, a.local),
		})
		porItem[a.itemID]++
	}
	if len(resultados) > limite {
		resultados = resultados[:limite]
	}
	return resultados
}

// recortarTrecho devolve uma janela de texto centrada na primeira ocorrência
// de qualquer uma das palavras buscadas.
func recortarTrecho(texto string, termos []string, larguraMax int) string {
	texto = limparEspacos(texto)
	runas := []rune(texto)
	if len(runas) <= larguraMax {
		return texto
	}
	baixo := semAcento(texto)
	melhorByte := -1
	for _, t := range termos {
		if t == "" {
			continue
		}
		if pos := strings.Index(baixo, t); pos >= 0 {
			if melhorByte == -1 || pos < melhorByte {
				melhorByte = pos
			}
		}
	}
	inicioRuna := 0
	if melhorByte > 0 {
		// Converte proporção aproximada de bytes para runas.
		posRuna := utf8.RuneCountInString(baixo[:melhorByte])
		if posRuna > larguraMax/3 {
			inicioRuna = posRuna - larguraMax/3
		}
	}
	if inicioRuna+larguraMax > len(runas) {
		inicioRuna = max(0, len(runas)-larguraMax)
	}
	// Ajusta para fronteira de espaço.
	if inicioRuna > 0 {
		for inicioRuna < len(runas) && !unicode.IsSpace(runas[inicioRuna]) {
			inicioRuna++
		}
		for inicioRuna < len(runas) && unicode.IsSpace(runas[inicioRuna]) {
			inicioRuna++
		}
	}
	fimRuna := min(len(runas), inicioRuna+larguraMax)
	if fimRuna < len(runas) {
		for fimRuna > inicioRuna && !unicode.IsSpace(runas[fimRuna-1]) {
			fimRuna--
		}
	}
	recorte := strings.TrimSpace(string(runas[inicioRuna:fimRuna]))
	if inicioRuna > 0 {
		recorte = "…" + recorte
	}
	if fimRuna < len(runas) {
		recorte = recorte + "…"
	}
	return recorte
}

// --- Extratores de texto por formato (EPUB, PDF, MOBI, AZW3, TXT)

const tamanhoBlocoTexto = 3200

// limiteLeituraLivro evita carregar arquivos enormes na memória do servidor
// (o Lab 0 tem 2 GB). Vale para PDF sem pdftotext, MOBI e AZW3.
const limiteLeituraLivro = 150 << 20

func extrairTrechosLivro(ctx context.Context, caminho, nome string) ([]trechoLivro, error) {
	switch strings.ToLower(filepath.Ext(nome)) {
	case ".epub":
		return extrairTrechosEPUB(caminho)
	case ".pdf":
		return extrairTrechosPDF(ctx, caminho)
	case ".mobi", ".azw3":
		return extrairTrechosPalmDOC(caminho)
	case ".txt", ".md":
		dados, err := os.ReadFile(caminho)
		if err != nil {
			return nil, err
		}
		return fatiarEmTrechos(string(dados), "Trecho"), nil
	default:
		return nil, nil
	}
}

func extrairTrechosEPUB(caminho string) ([]trechoLivro, error) {
	zr, err := zip.OpenReader(caminho)
	if err != nil {
		return nil, err
	}
	defer zr.Close()

	var trechos []trechoLivro
	secao := 0
	for _, f := range zr.File {
		nome := strings.ToLower(f.Name)
		if strings.HasPrefix(nome, "meta-inf/") || strings.HasSuffix(nome, "toc.ncx") {
			continue
		}
		if !strings.HasSuffix(nome, ".xhtml") && !strings.HasSuffix(nome, ".html") && !strings.HasSuffix(nome, ".htm") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		dados, err := io.ReadAll(io.LimitReader(rc, 4<<20))
		rc.Close()
		if err != nil || len(dados) == 0 {
			continue
		}
		tituloCap, texto := extrairTextoHTML(bytes.NewReader(dados))
		if texto == "" {
			continue
		}
		secao++
		rotulo := fmt.Sprintf("Seção %d", secao)
		if tituloCap != "" && utf8.RuneCountInString(tituloCap) <= 70 {
			rotulo = tituloCap
		}
		for _, p := range dividirTextoRunas(texto, tamanhoBlocoTexto) {
			trechos = append(trechos, trechoLivro{Local: rotulo, Texto: p})
		}
	}
	return trechos, nil
}

func extrairTrechosPDF(ctx context.Context, caminho string) ([]trechoLivro, error) {
	if bin, err := exec.LookPath("pdftotext"); err == nil {
		tctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		out, err := exec.CommandContext(tctx, bin, "-enc", "UTF-8", "-q", caminho, "-").Output()
		if err == nil && len(out) > 0 {
			paginas := strings.Split(string(out), "\f")
			var trechos []trechoLivro
			for idx, pg := range paginas {
				limpo := limparEspacos(pg)
				if limpo == "" {
					continue
				}
				rotulo := fmt.Sprintf("Página %d", idx+1)
				for _, parte := range dividirTextoRunas(limpo, tamanhoBlocoTexto) {
					trechos = append(trechos, trechoLivro{Local: rotulo, Texto: parte})
				}
			}
			if len(trechos) > 0 {
				return trechos, nil
			}
		}
	}
	return extrairTrechosPDFPuro(caminho)
}

// extrairTrechosPDFPuro extrai texto de fluxos PDF (/FlateDecode e literais)
// sem depender de programas externos.
func extrairTrechosPDFPuro(caminho string) ([]trechoLivro, error) {
	if info, err := os.Stat(caminho); err != nil {
		return nil, err
	} else if info.Size() > limiteLeituraLivro {
		return nil, fmt.Errorf("PDF de %d MB acima do limite de leitura direta", info.Size()>>20)
	}
	dados, err := os.ReadFile(caminho)
	if err != nil {
		return nil, err
	}
	var textos []string
	pos := 0
	for {
		i := bytes.Index(dados[pos:], []byte("stream"))
		if i < 0 {
			break
		}
		inicio := pos + i + 6
		if inicio < len(dados) && dados[inicio] == '\r' {
			inicio++
		}
		if inicio < len(dados) && dados[inicio] == '\n' {
			inicio++
		} else {
			pos = inicio
			continue
		}
		fimRel := bytes.Index(dados[inicio:], []byte("endstream"))
		if fimRel < 0 {
			break
		}
		bloco := dados[inicio : inicio+fimRel]
		pos = inicio + fimRel + 9

		conteudo := bloco
		if zr, err := zlib.NewReader(bytes.NewReader(bloco)); err == nil {
			descomp, _ := io.ReadAll(io.LimitReader(zr, 4<<20))
			zr.Close()
			if len(descomp) > 0 {
				conteudo = descomp
			}
		}
		if t := extrairOperadoresTextoPDF(conteudo); t != "" {
			textos = append(textos, t)
		}
	}
	// Sem pdftotext não há como saber a página: cada fluxo pode ser fonte,
	// imagem ou parte de página. O texto vira trechos sem número de página.
	return fatiarEmTrechos(strings.Join(textos, " "), "Trecho"), nil
}

func extrairOperadoresTextoPDF(fluxo []byte) string {
	if !bytes.Contains(fluxo, []byte("BT")) {
		return ""
	}
	var b strings.Builder
	i := 0
	n := len(fluxo)
	for i < n {
		if fluxo[i] == '(' {
			i++
			prof := 1
			for i < n && prof > 0 {
				c := fluxo[i]
				if c == '\\' && i+1 < n {
					i += 2
					continue
				}
				if c == '(' {
					prof++
				} else if c == ')' {
					prof--
					if prof == 0 {
						break
					}
				}
				if c >= 32 && c < 127 {
					b.WriteByte(c)
				} else if c >= 160 {
					b.WriteRune(rune(c))
				}
				i++
			}
			b.WriteByte(' ')
		}
		i++
	}
	return limparEspacos(b.String())
}

// extrairTrechosPalmDOC lê arquivos MOBI e AZW3 (PalmDB com compressão PalmDOC LZ77).
func extrairTrechosPalmDOC(caminho string) ([]trechoLivro, error) {
	if info, err := os.Stat(caminho); err != nil {
		return nil, err
	} else if info.Size() > limiteLeituraLivro {
		return nil, fmt.Errorf("arquivo de %d MB acima do limite de leitura", info.Size()>>20)
	}
	dados, err := os.ReadFile(caminho)
	if err != nil {
		return nil, err
	}
	if len(dados) < 86 {
		return nil, nil
	}
	numRecs := int(binary.BigEndian.Uint16(dados[76:78]))
	if numRecs < 2 || 78+numRecs*8 > len(dados) {
		return nil, nil
	}
	offsets := make([]int, numRecs+1)
	for i := 0; i < numRecs; i++ {
		off := int(binary.BigEndian.Uint32(dados[78+i*8 : 82+i*8]))
		if off < 0 || off > len(dados) {
			return nil, nil
		}
		offsets[i] = off
	}
	offsets[numRecs] = len(dados)
	if offsets[1]-offsets[0] < 16 {
		return nil, nil
	}
	rec0 := dados[offsets[0]:offsets[1]]
	compressao := binary.BigEndian.Uint16(rec0[0:2])
	qtdTexto := int(binary.BigEndian.Uint16(rec0[8:10]))
	if qtdTexto <= 0 || qtdTexto >= numRecs {
		qtdTexto = numRecs - 1
	}

	var bruto bytes.Buffer
	for r := 1; r <= qtdTexto && r < numRecs; r++ {
		if offsets[r] >= offsets[r+1] {
			continue
		}
		bloco := dados[offsets[r]:offsets[r+1]]
		switch compressao {
		case 1:
			bruto.Write(bloco)
		case 2:
			bruto.Write(descompactarPalmDOC(bloco))
		default:
			return nil, nil
		}
		if bruto.Len() > 12<<20 {
			break
		}
	}
	_, texto := extrairTextoHTML(bytes.NewReader(bruto.Bytes()))
	if texto == "" {
		texto = limparEspacos(bruto.String())
	}
	return fatiarEmTrechos(texto, "Trecho"), nil
}

func descompactarPalmDOC(in []byte) []byte {
	out := make([]byte, 0, len(in)*2)
	i, n := 0, len(in)
	for i < n {
		b := in[i]
		i++
		switch {
		case b == 0 || (b >= 0x09 && b <= 0x7f):
			out = append(out, b)
		case b >= 0x01 && b <= 0x08:
			fim := min(n, i+int(b))
			out = append(out, in[i:fim]...)
			i = fim
		case b >= 0x80 && b <= 0xbf:
			if i >= n {
				break
			}
			b2 := in[i]
			i++
			dist := ((int(b) & 0x3f) << 5) | (int(b2) >> 3)
			comp := (int(b2) & 0x07) + 3
			if dist > 0 && dist <= len(out) {
				for k := 0; k < comp; k++ {
					out = append(out, out[len(out)-dist])
				}
			}
		default:
			out = append(out, ' ', b^0x80)
		}
	}
	return out
}

func extrairTextoHTML(r io.Reader) (titulo string, texto string) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", ""
	}
	var b strings.Builder
	var percorrer func(*html.Node)
	percorrer = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "svg":
				return
			case "title", "h1", "h2", "h3":
				if titulo == "" {
					if t := limparEspacos(textoPlanoHTML(n)); t != "" {
						titulo = t
					}
				}
			case "p", "div", "li", "tr", "br", "h4", "h5", "h6", "blockquote", "section", "article":
				b.WriteByte(' ')
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			b.WriteByte(' ')
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			percorrer(c)
		}
	}
	percorrer(doc)
	return titulo, limparEspacos(b.String())
}

func textoPlanoHTML(n *html.Node) string {
	var b strings.Builder
	var ler func(*html.Node)
	ler = func(no *html.Node) {
		if no.Type == html.TextNode {
			b.WriteString(no.Data)
		}
		for c := no.FirstChild; c != nil; c = c.NextSibling {
			ler(c)
		}
	}
	ler(n)
	return b.String()
}

func limparEspacos(s string) string {
	return strings.TrimSpace(reEspaco.ReplaceAllString(s, " "))
}

func dividirTextoRunas(texto string, tamanho int) []string {
	runas := []rune(texto)
	if len(runas) == 0 {
		return nil
	}
	var partes []string
	for inicio := 0; inicio < len(runas); {
		fim := min(len(runas), inicio+tamanho)
		if fim < len(runas) {
			for fim > inicio+tamanho/2 && !unicode.IsSpace(runas[fim-1]) {
				fim--
			}
		}
		p := strings.TrimSpace(string(runas[inicio:fim]))
		if p != "" {
			partes = append(partes, p)
		}
		inicio = fim
	}
	return partes
}

func fatiarEmTrechos(texto, prefixo string) []trechoLivro {
	partes := dividirTextoRunas(limparEspacos(texto), tamanhoBlocoTexto)
	out := make([]trechoLivro, 0, len(partes))
	for i, p := range partes {
		out = append(out, trechoLivro{Local: fmt.Sprintf("%s %d", prefixo, i+1), Texto: p})
	}
	return out
}
