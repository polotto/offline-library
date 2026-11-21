package lab

import (
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestPrepararArtigoWikiPreservaConteudo(t *testing.T) {
	const original = `<head><link rel="stylesheet" href="./tema.css"><script src="./original.js"></script></head>
<body><aside>Menu original</aside><main><h1>Curitiba</h1><div id="mw-content-text">
<img src="./foto.jpg"><a href="./Paraná#História">Paraná</a>
<h2 id="História">História</h2><table class="infobox"><tr><td>Paraná</td></tr></table>
<table><tr><td>População</td></tr></table></div></main><div class="zim-footer">Licença</div></body>`
	pagina, err := html.Parse(strings.NewReader(original))
	if err != nil || !prepararWiki(pagina, false) {
		t.Fatalf("artigo não preparado: %v", err)
	}
	var saida strings.Builder
	if err := html.Render(&saida, pagina); err != nil {
		t.Fatal(err)
	}
	html := saida.String()
	for _, esperado := range []string{
		`name="viewport"`, `href="./tema.css"`, `src="./foto.jpg"`, `href="./Paraná#História"`,
		`href="#História"`, `class="lab-wiki-ficha"`, `class="lab-wiki-tabela"`, `>População<`, `>Licença<`,
	} {
		if !strings.Contains(html, esperado) {
			t.Errorf("ausente: %s", esperado)
		}
	}
	if strings.Contains(html, "Menu original") || strings.Contains(html, "<script") {
		t.Error("a interface original permaneceu")
	}
}

func TestAdaptarWikiSoParaDocumentos(t *testing.T) {
	for _, caso := range []struct {
		metodo, caminho, destino string
		adaptar                  bool
	}{
		{"GET", "/wiki/content/wiktionary_pt_2026-09/saudade", "document", true},
		{"GET", "/wiki/search?pattern=saudade", "document", true},
		{"GET", "/wiki/content/wikipedia_pt_2026-05/Curitiba", "iframe", false},
		{"GET", "/wiki/content/wikipedia_pt_2026-05/Curitiba?original=1", "document", false},
		{"GET", "/wiki/content/wikipedia_pt_2026-05/foto.jpg", "image", false},
		{"HEAD", "/wiki/search", "", false},
	} {
		r := httptest.NewRequest(caso.metodo, caso.caminho, nil)
		r.Header.Set("Sec-Fetch-Dest", caso.destino)
		if adaptarWiki(r) != caso.adaptar {
			t.Errorf("adaptação inesperada: %+v", caso)
		}
	}
}
