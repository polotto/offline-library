package lab

import (
	"bytes"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/polotto/offline-library/web"
	"golang.org/x/net/html"
)

type colecaoWiki struct{ ID, Titulo, Grupo string }
type grupoWiki struct {
	Titulo   string
	Colecoes []colecaoWiki
}

var inicioWiki = template.Must(template.ParseFS(web.Arquivos, "wiki/index.html"))

func (s *Servidor) registrarEnciclopedia(m *http.ServeMux) {
	m.Handle("/wiki/", s.proxyKiwix())
	m.HandleFunc("GET /wiki/{$}", s.enciclopedia)
	assets, _ := fs.Sub(web.Arquivos, "wiki")
	m.Handle("GET /wiki/lab/", http.StripPrefix("/wiki/lab", http.FileServerFS(assets)))
}

func (s *Servidor) enciclopedia(w http.ResponseWriter, r *http.Request) {
	titulos := []string{"Enciclopédia", "Dicionários", "Outras coleções"}
	grupos := []grupoWiki{{Titulo: titulos[0]}, {Titulo: titulos[1]}, {Titulo: titulos[2]}}
	for _, zim := range ZimsProntos(s.cfg) {
		i := 2
		switch {
		case strings.HasPrefix(zim.Prefixo, "wikipedia_"):
			i = 0
		case strings.HasPrefix(zim.Prefixo, "wiktionary_"):
			i = 1
		}
		c := colecaoWiki{strings.TrimSuffix(zim.Arquivo, ".zim"), TituloColecao(zim.Prefixo), titulos[i]}
		grupos[i].Colecoes = append(grupos[i].Colecoes, c)
	}
	var colecoes []colecaoWiki
	grupos = slices.DeleteFunc(grupos, func(g grupoWiki) bool { return len(g.Colecoes) == 0 })
	for _, g := range grupos {
		slices.SortStableFunc(g.Colecoes, func(a, b colecaoWiki) int {
			portugues := func(c colecaoWiki) bool { return strings.Contains(c.ID, "_pt_") }
			switch {
			case portugues(a) && !portugues(b):
				return -1
			case !portugues(a) && portugues(b):
				return 1
			default:
				return strings.Compare(a.Titulo, b.Titulo)
			}
		})
		colecoes = append(colecoes, g.Colecoes...)
	}
	estado, _ := s.kiwix.Estado()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = inicioWiki.ExecuteTemplate(w, "index.html", struct {
		Colecoes   []colecaoWiki
		Grupos     []grupoWiki
		Disponivel bool
	}{colecoes, grupos, estado == "pronto"})
}

func adaptarWiki(r *http.Request) bool {
	destino := r.Header.Get("Sec-Fetch-Dest")
	if r.Method != http.MethodGet || (destino != "" && destino != "document") || r.URL.Query().Get("original") == "1" {
		return false
	}
	return r.URL.Path == "/wiki/search" ||
		strings.HasPrefix(r.URL.Path, "/wiki/content/wikipedia_") ||
		strings.HasPrefix(r.URL.Path, "/wiki/content/wiktionary_")
}

func respostaWiki(resp *http.Response) error {
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		return nil
	}
	resp.Header.Add("Vary", "Sec-Fetch-Dest")
	if !adaptarWiki(resp.Request) {
		return nil
	}
	const limite = 8 << 20
	if resp.ContentLength > limite {
		return nil
	}
	original := resp.Body
	dados, err := io.ReadAll(io.LimitReader(original, limite+1))
	if err != nil {
		return err
	}
	if len(dados) > limite {
		resp.Body = struct {
			io.Reader
			io.Closer
		}{io.MultiReader(bytes.NewReader(dados), original), original}
		return nil
	}
	_ = original.Close()
	resp.Body = io.NopCloser(bytes.NewReader(dados))
	pagina, err := html.Parse(bytes.NewReader(dados))
	if err != nil || !prepararWiki(pagina, resp.Request.URL.Path == "/wiki/search") {
		return nil
	}
	var saida bytes.Buffer
	if err := html.Render(&saida, pagina); err != nil {
		return nil
	}
	resp.ContentLength = int64(saida.Len())
	resp.Header.Set("Content-Length", strconv.Itoa(saida.Len()))
	for _, h := range []string{"ETag", "Last-Modified", "Accept-Ranges"} {
		resp.Header.Del(h)
	}
	resp.Header.Set("Cache-Control", "no-store")
	resp.Header.Set("Content-Security-Policy", "script-src 'none'; object-src 'none'; frame-src 'none'; "+
		"style-src 'self' 'unsafe-inline'; img-src 'self' data:; media-src 'self' data:; font-src 'self' data:; base-uri 'none'")
	resp.Body = io.NopCloser(&saida)
	return nil
}

func atributoWiki(n *html.Node, chave string) string {
	for _, a := range n.Attr {
		if a.Key == chave {
			return a.Val
		}
	}
	return ""
}

func acharWiki(n *html.Node, procura func(*html.Node) bool) *html.Node {
	if procura(n) {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if encontrado := acharWiki(c, procura); encontrado != nil {
			return encontrado
		}
	}
	return nil
}

func noWiki(tag string, atributos ...html.Attribute) *html.Node {
	return &html.Node{Type: html.ElementNode, Data: tag, Attr: atributos}
}

func textoWiki(n *html.Node, texto string) {
	n.AppendChild(&html.Node{Type: html.TextNode, Data: texto})
}

func prepararWiki(pagina *html.Node, busca bool) bool {
	elemento := func(tag string) func(*html.Node) bool {
		return func(n *html.Node) bool { return n.Type == html.ElementNode && n.Data == tag }
	}
	head, body := acharWiki(pagina, elemento("head")), acharWiki(pagina, elemento("body"))
	if head == nil || body == nil {
		return false
	}
	main := acharWiki(body, elemento("main"))
	if !busca && main == nil {
		return false
	}
	if acharWiki(head, func(n *html.Node) bool { return atributoWiki(n, "name") == "viewport" }) == nil {
		head.AppendChild(noWiki("meta", html.Attribute{Key: "name", Val: "viewport"},
			html.Attribute{Key: "content", Val: "width=device-width,initial-scale=1"}))
	}
	head.AppendChild(noWiki("link", html.Attribute{Key: "rel", Val: "stylesheet"},
		html.Attribute{Key: "href", Val: "/wiki/lab/leitura.css"}))
	if busca {
		main = noWiki("main")
		for body.FirstChild != nil {
			c := body.FirstChild
			body.RemoveChild(c)
			main.AppendChild(c)
		}
	} else {
		main.Parent.RemoveChild(main)
		rodape := acharWiki(body, func(n *html.Node) bool { return atributoWiki(n, "class") == "zim-footer" })
		if rodape != nil {
			rodape.Parent.RemoveChild(rodape)
			main.AppendChild(rodape)
		}
	}
	for body.FirstChild != nil {
		body.RemoveChild(body.FirstChild)
	}
	body.Attr = []html.Attribute{{Key: "class", Val: "lab-wiki"}}
	nav := noWiki("nav", html.Attribute{Key: "class", Val: "lab-wiki-nav"}, html.Attribute{Key: "aria-label", Val: "Navegação da enciclopédia"})
	link := noWiki("a", html.Attribute{Key: "href", Val: "/wiki/"})
	textoWiki(link, "Enciclopédia e dicionários")
	nav.AppendChild(link)
	body.AppendChild(nav)
	body.AppendChild(main)
	removerScriptsWiki(head)
	removerScriptsWiki(main)
	if !busca {
		adicionarSumarioWiki(main)
		conterTabelasWiki(main)
	}
	return true
}

func conterTabelasWiki(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		proximo := c.NextSibling
		if c.Type != html.ElementNode || c.Data != "table" {
			conterTabelasWiki(c)
			c = proximo
			continue
		}
		rolagem := noWiki("div", html.Attribute{Key: "class", Val: "lab-wiki-tabela"},
			html.Attribute{Key: "tabindex", Val: "0"}, html.Attribute{Key: "aria-label", Val: "Tabela com rolagem"})
		if slices.Contains(strings.Fields(atributoWiki(c, "class")), "infobox") {
			ficha := noWiki("details", html.Attribute{Key: "class", Val: "lab-wiki-ficha"})
			resumo := noWiki("summary")
			textoWiki(resumo, "Ficha do artigo")
			ficha.AppendChild(resumo)
			ficha.AppendChild(rolagem)
			n.InsertBefore(ficha, c)
		} else {
			n.InsertBefore(rolagem, c)
		}
		n.RemoveChild(c)
		rolagem.AppendChild(c)
		c = proximo
	}
}

func removerScriptsWiki(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		proximo := c.NextSibling
		if c.Type == html.ElementNode && c.Data == "script" {
			n.RemoveChild(c)
		} else {
			removerScriptsWiki(c)
		}
		c = proximo
	}
}

func adicionarSumarioWiki(main *html.Node) {
	lista := noWiki("ul")
	var percorrer func(*html.Node)
	percorrer = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "h2" {
			id := atributoWiki(n, "id")
			if id != "" {
				item := noWiki("li")
				link := noWiki("a", html.Attribute{Key: "href", Val: "#" + id})
				textoWiki(link, strings.ReplaceAll(id, "_", " "))
				item.AppendChild(link)
				lista.AppendChild(item)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			percorrer(c)
		}
	}
	conteudo := acharWiki(main, func(n *html.Node) bool { return atributoWiki(n, "id") == "mw-content-text" })
	if conteudo == nil {
		return
	}
	percorrer(conteudo)
	if lista.FirstChild == nil {
		return
	}
	sumario := noWiki("details", html.Attribute{Key: "class", Val: "lab-wiki-sumario"})
	resumo := noWiki("summary")
	textoWiki(resumo, "Neste texto")
	sumario.AppendChild(resumo)
	sumario.AppendChild(lista)
	conteudo.Parent.InsertBefore(sumario, conteudo)
}
