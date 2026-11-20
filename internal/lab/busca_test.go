package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// indiceTeste grava um busca.db pequeno com o mesmo esquema e a mesma
// ordem de id do gerador (chave = nome sem acento + "|" + ...).
func indiceTeste(t *testing.T, extra int) string {
	t.Helper()
	type linha struct {
		nome, tipo, ctx string
		lat, lon, peso  float64
	}
	linhas := []linha{
		{"Curitiba", "Cidade", "", -25.43, -49.27, 9},
		{"Rua XV de Novembro", "Rua", "Centro, Curitiba", -25.43, -49.27, 2},
		{"Rua XV de Novembro", "Rua", "Centro, Corumbá", -19.0, -57.6, 2},
		{"Rua XV de Novembro", "Rua", "Centro, Joinville", -26.3, -48.84, 2},
		{"Rua XV de Novembro, 100", "Endereço", "Centro, Curitiba", -25.43, -49.27, 1},
		{"Avenida Paulista", "Avenida ou rua principal", "Bela Vista, São Paulo", -23.56, -46.65, 2.5},
		{"Avenida Paulista", "Avenida ou rua principal", "Centro, Maringá", -23.4, -51.9, 2.5},
		{"Avenida Paulista, 1000", "Endereço", "Bela Vista, São Paulo", -23.56, -46.65, 1},
		{"Terminal Rodoviário Rita Maria", "Rodoviária", "Centro, Florianópolis", -27.59, -48.56, 3},
		{"Rua da Rodoviária", "Rua", "Centro, Florianópolis", -27.6, -48.55, 2},
		{"Florianópolis", "Cidade", "", -27.59, -48.55, 9},
		{"Russas", "Cidade", "", -4.94, -37.97, 7},
		{"Rua Ruy Barbosa", "Rua", "Centro, Curitiba", -25.44, -49.27, 2},
	}
	for i := range extra {
		linhas = append(linhas, linha{fmt.Sprintf("Rua %d", i), "Rua", "Bairro, Cidade", -10, -50, 2})
	}
	chave := func(l linha) string {
		k := semAcento(l.nome) + "|" + l.tipo + "|" + l.ctx
		return k
	}
	sort.Slice(linhas, func(i, j int) bool { return chave(linhas[i]) < chave(linhas[j]) })
	caminho := filepath.Join(t.TempDir(), "busca.db")
	db, err := abrirSQLite(caminho, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE lugares(id INTEGER PRIMARY KEY, nome TEXT, detalhe TEXT, tipo TEXT, lat REAL, lon REAL, peso REAL)`,
		`CREATE VIRTUAL TABLE lugares_fts USING fts5(nome, detalhe, content='lugares', content_rowid='id',
		   tokenize='unicode61 remove_diacritics 2', prefix='2 3')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	tx, _ := db.Begin()
	for _, l := range linhas {
		detalhe := l.tipo
		if l.ctx != "" {
			detalhe += " · " + l.ctx
		}
		if _, err := tx.Exec(`INSERT INTO lugares(nome, detalhe, tipo, lat, lon, peso) VALUES(?,?,?,?,?,?)`,
			l.nome, detalhe, l.tipo, l.lat, l.lon, l.peso); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO lugares_fts(lugares_fts) VALUES('rebuild')`); err != nil {
		t.Fatal(err)
	}
	return caminho
}

// buscaPronta abre o índice e espera o carregador em segundo plano.
func buscaPronta(t *testing.T, caminho string) *Busca {
	t.Helper()
	b := &Busca{caminho: caminho}
	t.Cleanup(b.Fechar)
	if b.abrir() == nil {
		t.Fatal("índice não abriu")
	}
	for i := 0; i < 200; i++ {
		if e := b.estado.Load(); e != nil && e.fama != nil {
			return b
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("carregador não terminou")
	return nil
}

func TestMontarConsulta(t *testing.T) {
	casos := map[string]string{
		"Rua XV de Novembro Curitiba": `"rua" AND "xv" AND "novembro" AND "curitiba"*`,
		"Av Paulista 1000":            `("av" OR "avenida") AND "paulista" AND "1000"*`,
		"Rodoviária de Florianópolis": `"rodoviaria" AND "florianopolis"*`,
		"Rua XV d":                    `"rua" AND "xv"`,
		"Rua XV de":                   `"rua" AND "xv" AND "de"*`,
		"av":                          `"av"*`,
		"Rua ":                        `"rua"`,
	}
	for texto, quer := range casos {
		if got := montarConsulta(texto).fts(); got != quer {
			t.Errorf("%q: fts = %s, quer %s", texto, got, quer)
		}
	}
}

func TestProcurarRelevancia(t *testing.T) {
	b := buscaPronta(t, indiceTeste(t, 0))
	if e := b.estado.Load(); !e.ordenado {
		t.Fatal("índice de teste deveria estar em ordem")
	}
	curitiba := [2]float64{-25.43, -49.27}
	longe := [2]float64{-20, -59}
	casos := []struct {
		texto   string
		centro  [2]float64
		nome    string
		detalhe string
	}{
		{"Rua XV de Novembro Curitiba", curitiba, "Rua XV de Novembro", "Curitiba"},
		{"Rua XV de Novembro Curitiba", longe, "Rua XV de Novembro", "Curitiba"},
		{"Rua XV d", curitiba, "Rua XV de Novembro", "Curitiba"},
		{"Rua XV d", longe, "Rua XV de Novembro", "Corumbá"},
		{"Av Paulista 1000", curitiba, "Avenida Paulista, 1000", "São Paulo"},
		{"Av Paulista", longe, "Avenida Paulista", ""},
		{"Rodoviaria de Florianopolis", curitiba, "Terminal Rodoviário Rita Maria", "Florianópolis"},
		{"Rodoviária de Florianópolis", longe, "Terminal Rodoviário Rita Maria", "Florianópolis"},
		{"Curitiba", longe, "Curitiba", ""},
		{"Ru", curitiba, "", "Curitiba"},
		{"Rua XV de Novembro Curitiba", [2]float64{-25.43, -49.2701}, "Rua XV de Novembro", "Curitiba"},
		{"Rua XV de Novembro 100", curitiba, "Rua XV de Novembro, 100", "Curitiba"},
		{"r xv", curitiba, "Rua XV de Novembro", "Curitiba"},
	}
	for _, c := range casos {
		lista, _, err := b.Procurar(context.Background(), c.texto, c.centro[0], c.centro[1], true, 12)
		if err != nil {
			t.Fatalf("%q: %v", c.texto, err)
		}
		if len(lista) == 0 {
			t.Errorf("%q %v: nada achado", c.texto, c.centro)
			continue
		}
		if (c.nome != "" && lista[0].Nome != c.nome) || !strings.Contains(lista[0].Detalhe, c.detalhe) {
			t.Errorf("%q %v: primeiro = %s (%s), quer %s (%s)", c.texto, c.centro, lista[0].Nome, lista[0].Detalhe, c.nome, c.detalhe)
		}
	}
}

func TestJuntarRepetidos(t *testing.T) {
	lista := []Lugar{
		{Nome: "Rua XV de Novembro", Detalhe: "Rua · Centro, Curitiba", Lat: -25.43, Lon: -49.27},
		{Nome: "Rua XV de Novembro", Detalhe: "Avenida ou rua principal · Centro, Curitiba", Lat: -25.431, Lon: -49.26},
		{Nome: "Rua XV de Novembro", Detalhe: "Rua · Centro, Joinville", Lat: -26.3, Lon: -48.84},
		{Nome: "Rua XV de Novembro, 100", Detalhe: "Endereço · Centro, Curitiba", Lat: -25.43, Lon: -49.27},
	}
	out := juntarRepetidos(lista, 12)
	if len(out) != 3 || out[1].Detalhe != "Rua · Centro, Joinville" {
		t.Fatalf("out = %v", out)
	}
	if len(juntarRepetidos(lista, 2)) != 2 {
		t.Fatal("limite não respeitado")
	}
}

func TestFaixaNomeLimiteEParcial(t *testing.T) {
	old := limiteFaixa
	limiteFaixa = 50
	defer func() { limiteFaixa = old }()
	b := buscaPronta(t, indiceTeste(t, 300))
	lista, parcial, err := b.Procurar(context.Background(), "Rua", -25.43, -49.27, true, 12)
	if err != nil || len(lista) == 0 || !parcial {
		t.Fatalf("lista=%d parcial=%v err=%v", len(lista), parcial, err)
	}
}

func TestProcurarParaQuandoClienteDesiste(t *testing.T) {
	b := buscaPronta(t, indiceTeste(t, 0))
	ctx, cancelar := context.WithCancel(context.Background())
	cancelar()
	if _, _, err := b.Procurar(ctx, "Rua XV de Novembro", 0, 0, false, 12); err == nil {
		t.Fatal("esperava erro de contexto cancelado")
	}
}

func TestOrcamentoEsgotadoDevolveParcial(t *testing.T) {
	old := orcamentoBusca
	orcamentoBusca = time.Nanosecond
	defer func() { orcamentoBusca = old }()
	b := buscaPronta(t, indiceTeste(t, 0))
	lista, parcial, err := b.Procurar(context.Background(), "Curitiba", 0, 0, false, 12)
	if err != nil || !parcial {
		t.Fatalf("parcial=%v err=%v", parcial, err)
	}
	// A cidade vem da memória, mesmo sem tempo para o disco.
	if len(lista) == 0 || lista[0].Nome != "Curitiba" {
		t.Fatalf("lista = %v", lista)
	}
}

func TestHandlers(t *testing.T) {
	b := buscaPronta(t, indiceTeste(t, 0))
	pedir := func(h http.HandlerFunc, url string) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", url, nil))
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}
	if code, m := pedir(b.handlerPronta, "/mapas/busca/pronta"); code != 200 || m["pronta"] != true {
		t.Fatalf("pronta: %d %v", code, m)
	}
	code, m := pedir(b.handler, "/mapas/busca?q=Curitiba&lat=-25.43&lon=-49.27")
	if code != 200 || len(m["lugares"].([]any)) == 0 {
		t.Fatalf("busca: %d %v", code, m)
	}
	if code, m := pedir(b.handler, "/mapas/busca?q=C"); code != 200 || len(m["lugares"].([]any)) != 0 {
		t.Fatalf("uma letra: %d %v", code, m)
	}
	sem := &Busca{caminho: filepath.Join(t.TempDir(), "nao-existe.db")}
	if code, m := pedir(sem.handlerPronta, "/mapas/busca/pronta"); code != 503 || m["pronta"] != false {
		t.Fatalf("pronta sem índice: %d %v", code, m)
	}
	if code, _ := pedir(sem.handler, "/mapas/busca?q=Curitiba"); code != 503 {
		t.Fatalf("busca sem índice: %d", code)
	}
}

// prefixos devolve o texto digitado letra a letra, a partir de 2 letras.
func prefixos(s string) []string {
	r := []rune(s)
	var out []string
	for i := 2; i <= len(r); i++ {
		if r[i-1] == ' ' {
			continue
		}
		out = append(out, string(r[:i]))
	}
	return out
}

// TestMedirBuscaReal mede o índice real. Só roda com BUSCA_DB apontando
// para o arquivo. BUSCA_CONSULTAS (separadas por "|") troca a lista;
// BUSCA_PREFIXOS=1 mede também cada prefixo digitado.
func TestMedirBuscaReal(t *testing.T) {
	caminho := os.Getenv("BUSCA_DB")
	if caminho == "" {
		t.Skip("BUSCA_DB não definido")
	}
	ini := time.Now()
	b := &Busca{caminho: caminho}
	defer b.Fechar()
	b.abrir()
	for os.Getenv("BUSCA_SEM_ESPERA") != "1" && (b.estado.Load() == nil || b.estado.Load().fama == nil) {
		time.Sleep(20 * time.Millisecond)
		if time.Since(ini) > 5*time.Minute {
			t.Fatal("carregador não terminou")
		}
	}
	e := b.estado.Load()
	if e == nil {
		e = &indiceBusca{}
	}
	runtime.GC()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	t.Logf("carregador: %v, ordenado=%v, fortes=%d, famosas=%d, heap=%d MB", time.Since(ini).Round(time.Millisecond), e.ordenado, len(e.fortes), len(e.fama), mem.HeapAlloc>>20)
	if os.Getenv("BUSCA_ETAPAS") == "1" {
		medirEtapa = func(nome string, d time.Duration, err error) {
			t.Logf("    etapa %-28q %7.1f ms %v", nome, float64(d.Microseconds())/1000, err)
		}
		defer func() { medirEtapa = nil }()
	}
	consultas := []string{"Rua XV de Novembro Curitiba", "Av Paulista 1000", "Rodoviaria de Florianopolis", "Ru", "Rua"}
	if v := os.Getenv("BUSCA_CONSULTAS"); v != "" {
		consultas = strings.Split(v, "|")
	}
	if os.Getenv("BUSCA_PREFIXOS") == "1" {
		var todas []string
		for _, q := range consultas {
			todas = append(todas, prefixos(q)...)
		}
		consultas = todas
	}
	// Cliente que desiste: a consulta precisa parar logo.
	for _, q := range []string{"Ro", "Rua XV de", "Av Paulista 1000"} {
		ctx, cancelar := context.WithTimeout(context.Background(), 5*time.Millisecond)
		ini := time.Now()
		_, _, err := b.Procurar(ctx, q, -25.43, -49.27, true, 12)
		cancelar()
		t.Logf("desistência %-12q parou em %v (%v)", q, time.Since(ini).Round(time.Millisecond/10), err)
	}
	for _, centro := range [][2]float64{{-25.43, -49.27}, {-20, -59}} {
		var pior time.Duration
		for _, q := range consultas {
			ini := time.Now()
			lista, parcial, err := b.Procurar(context.Background(), q, centro[0], centro[1], true, 12)
			d := time.Since(ini)
			pior = max(pior, d)
			primeiro := ""
			if len(lista) > 0 {
				primeiro = lista[0].Nome + " | " + lista[0].Detalhe
			}
			t.Logf("%v %-30q %7.1f ms parcial=%-5v %v %s", centro, q, float64(d.Microseconds())/1000, parcial, err, primeiro)
			if os.Getenv("BUSCA_TOP") == "1" {
				for i, l := range lista {
					t.Logf("      %2d %6.2f %s | %s", i+1, l.nota, l.Nome, l.Detalhe)
				}
			}
		}
		t.Logf("%v pior: %v", centro, pior)
		b.respMu.Lock()
		b.respostas, b.ordemResp = nil, nil // sem cache de resposta entre centros
		b.respMu.Unlock()
	}
}

func TestCaminhoRapidoIgualAoLento(t *testing.T) {
	for _, s := range []string{"Rua XV de Novembro, 100", "  Av.  Paulista\t1000 ", "São Paulo", "R. d'Oeste-2", "", "A|B"} {
		if a, b := semAcentoRapido(s), semAcento(s); a != b {
			t.Errorf("semAcentoRapido(%q) = %q, quer %q", s, a, b)
		}
		lento := " " + strings.Join(palavras(s), " ") + " "
		if lento == "  " {
			lento = " "
		}
		if a := normalPalavras(s); a != lento {
			t.Errorf("normalPalavras(%q) = %q, quer %q", s, a, lento)
		}
	}
}
