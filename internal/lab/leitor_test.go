package lab

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestApontamentosPorContaEArquivo(t *testing.T) {
	c, err := AbrirCatalogo(&Config{Dados: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Fechar)
	for _, sql := range []string{
		`INSERT INTO usuarios VALUES ('leitor', ''), ('outro', '')`,
		`INSERT INTO itens(id,titulo,criado,alterado) VALUES (1,'Livro','','')`,
		`INSERT INTO arquivos(id,item_id,nome,caminho,tamanho,criado)
			VALUES (1,1,'Livro.pdf','',0,''), (2,1,'Livro.epub','',0,'')`,
	} {
		if _, err := c.db.Exec(sql); err != nil {
			t.Fatal(err)
		}
	}
	mux := http.NewServeMux()
	c.Registrar(mux)

	requisicao := func(metodo, arquivo, conta string, dados url.Values) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(metodo, "http://lab.test/catalogo/ler/"+arquivo, strings.NewReader(dados.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if conta != "" {
			sessao := conta + "|" + strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
			r.AddCookie(&http.Cookie{Name: "lab_sessao", Value: sessao + "|" + c.assinar(sessao)})
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	criar := url.Values{"acao": {"criar"}, "posicao": {"3"}, "texto": {"Texto privado de teste"}}
	if w := requisicao("POST", "1", "leitor", criar); w.Code != 303 {
		t.Fatalf("criar: %d %s", w.Code, w.Body)
	}
	for _, caso := range []struct {
		arquivo, conta string
		visivel        bool
	}{{"1", "leitor", true}, {"1", "outro", false}, {"1", "", false}, {"2", "leitor", false}} {
		w := requisicao("GET", caso.arquivo, caso.conta, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "Texto privado de teste") != caso.visivel {
			t.Errorf("escopo de arquivo e conta incorreto: %+v, status %d", caso, w.Code)
		}
	}
	for _, acao := range []string{"editar", "apagar"} {
		form := url.Values{"acao": {acao}, "nota": {"1"}, "texto": {"alteração"}}
		for _, escopo := range [][2]string{{"1", "outro"}, {"2", "leitor"}} {
			if w := requisicao("POST", escopo[0], escopo[1], form); w.Code != 404 {
				t.Errorf("%s fora do escopo: %d", acao, w.Code)
			}
		}
	}
	editar := url.Values{"acao": {"editar"}, "nota": {"1"}, "texto": {"Texto revisado"}}
	if w := requisicao("POST", "1", "leitor", editar); w.Code != 303 {
		t.Fatalf("editar como autor: %d %s", w.Code, w.Body)
	}
	notas, err := c.apontamentos(1, "leitor")
	if err != nil || len(notas) != 1 || notas[0].Texto != "Texto revisado" || notas[0].Posicao != "3" {
		t.Fatalf("edição alterou a posição ou não guardou o texto: %v %v", notas, err)
	}
	apagar := url.Values{"acao": {"apagar"}, "nota": {"1"}}
	if w := requisicao("POST", "1", "leitor", apagar); w.Code != 303 {
		t.Fatalf("apagar como autor: %d %s", w.Code, w.Body)
	}
	notas, err = c.apontamentos(1, "leitor")
	if err != nil || len(notas) != 0 {
		t.Fatalf("apontamento permaneceu após exclusão: %v %v", notas, err)
	}
	if w := requisicao("POST", "1", "leitor", criar); w.Code != 303 {
		t.Fatalf("criar antes da remoção: %d %s", w.Code, w.Body)
	}
	if _, err := c.db.Exec(`DELETE FROM arquivos WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	notas, err = c.apontamentos(1, "leitor")
	if err != nil || len(notas) != 0 {
		t.Fatalf("remoção do arquivo: %v %v", notas, err)
	}
}
