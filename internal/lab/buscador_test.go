package lab

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func criarEPUB(t *testing.T, caminho string, capitulos map[string]string) {
	t.Helper()
	f, err := os.Create(caminho)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for nome, corpo := range capitulos {
		w, err := zw.Create("OEBPS/" + nome)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(corpo))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestBuscarConteudoDosLivros(t *testing.T) {
	c, err := AbrirCatalogo(&Config{Dados: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Fechar)
	pasta := filepath.Join(c.dir, "arquivos")
	criarEPUB(t, filepath.Join(pasta, "publico.epub"), map[string]string{
		"cap1.xhtml": `<html><body><h1>Capítulo da floresta</h1><p>O jabuti atravessou o rio devagar.</p></body></html>`,
	})
	criarEPUB(t, filepath.Join(pasta, "privado.epub"), map[string]string{
		"cap1.xhtml": `<html><body><h1>Diário</h1><p>O jabuti secreto dormiu cedo.</p></body></html>`,
	})
	for _, sql := range []string{
		`INSERT INTO itens(id,titulo,autores,publico,criado,alterado) VALUES (1,'Contos','Ana',1,'',''), (2,'Diário','Bia',0,'','')`,
		`INSERT INTO arquivos(id,item_id,nome,caminho,tamanho,criado) VALUES
			(1,1,'Contos.epub','publico.epub',10,''), (2,2,'Diario.epub','privado.epub',10,'')`,
	} {
		if _, err := c.db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.IndexarConteudoPendente(context.Background()); err != nil {
		t.Fatal(err)
	}

	res := c.BuscarLivros("jabuti", false, 8)
	if len(res) != 1 || res[0].ItemID != 1 {
		t.Fatalf("visitante deve achar só o livro público: %+v", res)
	}
	if res[0].Local != "Capítulo da floresta" || !strings.Contains(res[0].Trecho, "jabuti") {
		t.Errorf("trecho ou capítulo errado: %+v", res[0])
	}
	if res[0].URLLeitura != "/catalogo/ler/1" {
		t.Errorf("link de leitura: %q", res[0].URLLeitura)
	}
	if res := c.BuscarLivros("jabuti", true, 8); len(res) != 2 {
		t.Errorf("conta logada deve achar os dois: %+v", res)
	}
	if res := c.BuscarLivros("jabuti rio", false, 8); len(res) != 1 {
		t.Errorf("busca com duas palavras: %+v", res)
	}

	// Arquivo apagado sai do índice na rodada seguinte.
	if _, err := c.db.Exec(`DELETE FROM arquivos WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := c.IndexarConteudoPendente(context.Background()); err != nil {
		t.Fatal(err)
	}
	if res := c.BuscarLivros("atravessou", true, 8); len(res) != 0 {
		t.Errorf("arquivo apagado continua na busca: %+v", res)
	}
}

func TestResultadosKiwixHTML(t *testing.T) {
	pagina := `<html><body><div class="results"><ul>
		<li><a href="/wiki/content/wikipedia_pt_all/A/Curitiba">Curitiba</a><cite>Capital do <b>Paraná</b>.</cite></li>
		<li><a href="https://externo.test/x">Fora</a></li>
		<li><a href="/wiki/content/wikipedia_pt_all/A/Paran%C3%A1">Paraná</a></li>
	</ul></div></body></html>`
	res := extrairResultadosKiwixHTML(strings.NewReader(pagina), "Wikipédia", 5)
	if len(res) != 2 {
		t.Fatalf("esperava 2 resultados locais: %+v", res)
	}
	if res[0].Titulo != "Curitiba" || res[0].Trecho != "Capital do Paraná." || res[0].URL != "/wiki/content/wikipedia_pt_all/A/Curitiba?leitura=1" {
		t.Errorf("primeiro resultado: %+v", res[0])
	}
}

func TestURLArtigoWikiSemSubirPasta(t *testing.T) {
	if u := montarURLArtigoWiki("wiki_pt", "A/../../segredo"); u != "/wiki/content/wiki_pt/A/segredo?leitura=1" {
		t.Errorf("caminho não foi limpo: %q", u)
	}
}

func TestRecortarTrechoCentraNaPalavra(t *testing.T) {
	texto := strings.Repeat("palavra comum ", 60) + "jabuti no meio " + strings.Repeat("final longo ", 60)
	r := recortarTrecho(texto, []string{"jabuti"}, 120)
	if !strings.Contains(r, "jabuti") || !strings.HasPrefix(r, "…") || !strings.HasSuffix(r, "…") {
		t.Errorf("recorte: %q", r)
	}
}

func TestIndiceLivrosCorrompidoRefaz(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "livros.db")
	if err := os.WriteFile(caminho, []byte("isto não é um banco SQLite, só lixo gravado pelo disco"), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := abrirIndiceLivros(caminho)
	if err != nil {
		t.Fatalf("deveria refazer o índice: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO livros_fts(item_id, arquivo_id, local, texto) VALUES(1,1,'x','ok')`); err != nil {
		t.Fatal(err)
	}
}
