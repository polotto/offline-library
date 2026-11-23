package lab

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
)

func TestProcurarRapidoPrefixoEFTS(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "busca.db")
	db, err := abrirSQLite(caminho, false)
	if err != nil {
		t.Fatal(err)
	}
	lugares := []struct {
		nome, detalhe, tipo string
		peso                float64
	}{
		{"Curitiba", "Cidade · Paraná", "Cidade", 9},
		{"Curitibanos", "Cidade · Santa Catarina", "Cidade", 6},
		{"Rua Curitiba", "Rua · Centro, Londrina", "Rua", 1},
		{"Curi", "Bairro · Algum lugar", "Bairro", 1},
		{"Avenida Brasil", "Rua · Centro, Curitiba", "Rua", 2},
		{"Árvore", "Ponto · Praça", "Ponto", 1},
		{"Zebra", "Ponto · Zoológico", "Ponto", 1},
	}
	// Mesma ordem do gerar-busca: GROUP BY chave, que começa por semAcento(nome).
	sort.Slice(lugares, func(i, j int) bool {
		return semAcento(lugares[i].nome)+"|" < semAcento(lugares[j].nome)+"|"
	})
	for _, q := range []string{
		`CREATE TABLE lugares(id INTEGER PRIMARY KEY, nome TEXT, detalhe TEXT, tipo TEXT, lat REAL, lon REAL, peso REAL)`,
		`CREATE VIRTUAL TABLE lugares_fts USING fts5(nome, detalhe, content='lugares', content_rowid='id',
		   tokenize='unicode61 remove_diacritics 2', prefix='2 3')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range lugares {
		if _, err := db.Exec(`INSERT INTO lugares(nome, detalhe, tipo, lat, lon, peso) VALUES(?,?,?,?,?,?)`,
			l.nome, l.detalhe, l.tipo, -25.4, -49.2, l.peso); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO lugares_fts(lugares_fts) VALUES('rebuild')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	b := &Busca{caminho: caminho}
	defer b.Fechar()
	lista, err := b.ProcurarRapido(context.Background(), "curitiba", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(lista) < 4 || lista[0].Nome != "Curitiba" {
		t.Fatalf("esperava Curitiba primeiro e 4 resultados: %+v", lista)
	}
	nomes := map[string]bool{}
	for _, l := range lista {
		nomes[l.Nome] = true
	}
	for _, n := range []string{"Curitibanos", "Rua Curitiba", "Avenida Brasil"} {
		if !nomes[n] {
			t.Errorf("faltou %s em %+v", n, lista)
		}
	}
	if nomes["Curi"] || nomes["Zebra"] {
		t.Errorf("resultado indevido em %+v", lista)
	}
	if lista, _ := b.ProcurarRapido(context.Background(), "arvore", 5); len(lista) != 1 || lista[0].Nome != "Árvore" {
		t.Errorf("busca sem acento: %+v", lista)
	}
}

func TestCacheBuscaLimite(t *testing.T) {
	c := &cacheBusca{max: 2, ttl: 1e9, itens: map[string]entradaCache{}}
	c.guardar("a", 1)
	c.guardar("b", 2)
	c.guardar("c", 3)
	if _, ok := c.pegar("a"); ok {
		t.Error("a deveria ter saído do cache")
	}
	if v, ok := c.pegar("c"); !ok || v.(int) != 3 {
		t.Error("c deveria estar no cache")
	}
}
