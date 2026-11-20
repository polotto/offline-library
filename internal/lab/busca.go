package lab

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

// Busca consulta o índice de lugares em mapas/busca.db, gerado por
// "lab gerar-busca". Reabre o arquivo quando ele é trocado.
//
// A busca junta três fontes, todas baratas:
//   - faixa de nome: o gerador grava os lugares em ordem do nome sem
//     acento, então os nomes que começam pelo texto digitado formam uma
//     faixa contínua de id. Acha a faixa por busca binária e lê só ela.
//   - FTS sem ORDER BY rank: o rank pontua todas as linhas antes do
//     LIMIT, o que leva dezenas de segundos para "rua" ou "de". Sem ele,
//     a consulta para no limite. A nota é calculada aqui.
//   - lugares fortes em memória (cidades, bairros, aeroportos), para
//     prefixos curtos como "Ru". Carregados em segundo plano.
type Busca struct {
	caminho string
	mu      sync.Mutex
	db      *sql.DB
	versao  time.Time
	parar   context.CancelFunc
	estado  atomic.Pointer[indiceBusca]

	respMu    sync.Mutex
	respostas map[string]respostaBusca
	ordemResp []string
}

// indiceBusca guarda o que o carregador em segundo plano descobriu.
type indiceBusca struct {
	ordenado bool  // ids seguem a ordem do nome sem acento
	maxID    int64 // maior id de lugares
	fortes   []lugarForte
	// baldes liga as duas primeiras letras de cada palavra aos índices
	// em fortes que têm essa palavra. Evita varrer todos a cada tecla.
	baldes map[string][]int32
	// fama é o bônus de ruas com muitos endereços na mesma cidade
	// (Avenida Paulista em São Paulo, Rua XV em Curitiba), por id.
	fama map[int64]float64
}

// baldesDe monta os baldes de duas letras dos lugares fortes.
func baldesDe(fortes []lugarForte) map[string][]int32 {
	m := map[string][]int32{}
	for i := range fortes {
		x := &fortes[i].txt
		vistos := map[string]bool{}
		for _, campo := range []string{x.nome, x.tipo, x.bairro, x.cidade} {
			for _, w := range strings.Fields(campo) {
				if len(w) < 2 {
					continue
				}
				k := w[:2]
				if !vistos[k] {
					vistos[k] = true
					m[k] = append(m[k], int32(i))
				}
			}
		}
	}
	return m
}

// candidatosFortes devolve os índices em fortes que podem casar com a
// consulta: o balde do termo mais longo. nil quer dizer "todos".
func (e *indiceBusca) candidatosFortes(c consulta) ([]int32, bool) {
	if e.baldes == nil {
		return nil, false
	}
	melhor := -1
	for i, t := range c.termos {
		if len(t.texto) >= 2 && (melhor < 0 || len(t.texto) > len(c.termos[melhor].texto)) {
			melhor = i
		}
	}
	if melhor < 0 {
		return nil, false
	}
	t := c.termos[melhor]
	lista := e.baldes[t.texto[:2]]
	if t.extenso != "" && t.extenso[:2] != t.texto[:2] {
		lista = append(append([]int32(nil), lista...), e.baldes[t.extenso[:2]]...)
	}
	if t.outro != "" && t.outro[:2] != t.texto[:2] {
		lista = append(append([]int32(nil), lista...), e.baldes[t.outro[:2]]...)
	}
	return lista, true
}

type lugarForte struct {
	Lugar
	id  int64
	txt textoLugar
}

// textoLugar guarda as partes do lugar já sem acento, uma palavra por
// espaço (" rua xv de novembro "), para conferir os termos.
type textoLugar struct {
	nome, tipo, bairro, cidade string
}

func prepararTexto(l *Lugar) textoLugar {
	x := textoLugar{nome: normalPalavras(l.Nome), tipo: normalPalavras(l.Tipo)}
	// detalhe = "Tipo · Bairro, Cidade", "Tipo · Cidade" ou só "Tipo".
	if _, ctxto, ok := strings.Cut(l.Detalhe, " · "); ok {
		if i := strings.LastIndex(ctxto, ", "); i >= 0 {
			x.bairro, x.cidade = normalPalavras(ctxto[:i]), normalPalavras(ctxto[i+2:])
		} else {
			x.cidade = normalPalavras(ctxto)
		}
	}
	return x
}

type respostaBusca struct {
	lista   []Lugar
	parcial bool
}

// Ajustes medidos no índice real (8,9 milhões de lugares).
var (
	orcamentoBusca = 170 * time.Millisecond // tempo das fontes, desde a chegada
	folgaBusca     = 20 * time.Millisecond  // espera a mais pelas fontes que param
	limiteFaixa    = 2500                   // linhas lidas por faixa de nome
	limiteFTS      = 800                    // linhas lidas por consulta FTS
	pesoForte      = 3.0                    // peso mínimo para ficar em memória
	maxRespostas   = 256                    // respostas guardadas
)

// medirEtapa, se definido (só em teste), recebe o tempo de cada fonte.
var medirEtapa func(nome string, d time.Duration, err error)

func AbrirBusca(cfg *Config) *Busca {
	return &Busca{caminho: cfg.ArquivoBusca()}
}

func (b *Busca) abrir() *sql.DB {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, err := os.Stat(b.caminho)
	if err != nil {
		b.fecharTrava()
		return nil
	}
	if b.db != nil && st.ModTime().Equal(b.versao) {
		return b.db
	}
	b.fecharTrava()
	db, err := abrirSQLite(b.caminho, true)
	if err != nil {
		return nil
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	b.db, b.versao = db, st.ModTime()
	ctx, parar := context.WithCancel(context.Background())
	b.parar = parar
	go b.carregar(ctx, db)
	return db
}

func (b *Busca) pronta() bool { return b.abrir() != nil }

func (b *Busca) Fechar() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fecharTrava()
}

// fecharTrava fecha o banco. Chamar com b.mu travado.
func (b *Busca) fecharTrava() {
	if b.parar != nil {
		b.parar()
		b.parar = nil
	}
	if b.db != nil {
		b.db.Close()
		b.db = nil
	}
	b.estado.Store(nil)
	b.respMu.Lock()
	b.respostas, b.ordemResp = nil, nil
	b.respMu.Unlock()
}

// carregar confere a ordem dos ids e lê os lugares fortes. Roda uma vez
// por versão do arquivo; a busca funciona sem ele, só com menos fontes.
func (b *Busca) carregar(ctx context.Context, db *sql.DB) {
	est := &indiceBusca{}
	if err := db.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) FROM lugares`).Scan(&est.maxID); err != nil {
		return
	}
	est.ordenado = idsOrdenados(ctx, db, est.maxID)
	b.publicar(db, est)
	rows, err := db.QueryContext(ctx, `SELECT id, nome, detalhe, tipo, lat, lon, peso FROM lugares WHERE peso >= ?`, pesoForte)
	if err != nil {
		return
	}
	defer rows.Close()
	var fortes []lugarForte
	tipos := map[string][2]string{}
	for rows.Next() {
		var f lugarForte
		if rows.Scan(&f.id, &f.Nome, &f.Detalhe, &f.Tipo, &f.Lat, &f.Lon, &f.peso) != nil {
			return
		}
		f.txt = prepararTexto(&f.Lugar)
		// Tipos se repetem muito: guarda uma cópia só de cada.
		if t, ok := tipos[f.Tipo]; ok {
			f.Tipo, f.txt.tipo = t[0], t[1]
		} else {
			tipos[f.Tipo] = [2]string{f.Tipo, f.txt.tipo}
		}
		fortes = append(fortes, f)
	}
	if rows.Err() != nil {
		return
	}
	rows.Close()
	novo := *est
	novo.fortes = fortes
	novo.baldes = baldesDe(fortes)
	b.publicar(db, &novo)

	// Segunda volta, mais demorada: ruas famosas.
	famosas, fama, err := ruasFamosas(ctx, db)
	if err != nil {
		return
	}
	for i := range famosas {
		f := &famosas[i]
		if t, ok := tipos[f.Tipo]; ok {
			f.Tipo, f.txt.tipo = t[0], t[1]
		}
	}
	final := novo
	final.fortes = append(fortes[:len(fortes):len(fortes)], famosas...)
	final.baldes = baldesDe(final.fortes)
	final.fama = fama
	b.publicar(db, &final)
}

// Uma rua é famosa com pelo menos minEnderecosFama endereços na mesma
// cidade. O bônus cresce devagar: 2 × log2(n / 25).
const minEnderecosFama = 50

func bonusFama(n int) float64 { return 2 * math.Log2(float64(n)/25) }

// ruasFamosas conta os endereços ("Rua X, 123") por rua e cidade e
// devolve as linhas das ruas com muitos endereços. Usa a ordem dos ids:
// os endereços de uma rua vêm juntos, logo antes da própria rua
// ("rua x, 1" < "rua x|", porque "," vem antes de "|").
func ruasFamosas(ctx context.Context, db *sql.DB) ([]lugarForte, map[int64]float64, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, nome, detalhe, tipo, lat, lon, peso FROM lugares
		WHERE tipo IN ('Endereço', 'Rua', 'Avenida ou rua principal', 'Rodovia')`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	// pend guarda as ruas famosas cuja linha da própria rua ainda vem.
	type pendente struct {
		rua, cidade string
		n           int
		emMemoria   bool // um trecho já entrou nos fortes
	}
	var (
		famosas  []lugarForte
		fama     = map[int64]float64{}
		pend     []pendente
		ruaAtual string
		contagem = map[string]int{}
	)
	fecharRua := func() {
		if ruaAtual == "" {
			return
		}
		for cid, n := range contagem {
			if n >= minEnderecosFama {
				pend = append(pend, pendente{rua: ruaAtual, cidade: cid, n: n})
			}
		}
		clear(contagem)
	}
	for rows.Next() {
		var f lugarForte
		if err := rows.Scan(&f.id, &f.Nome, &f.Detalhe, &f.Tipo, &f.Lat, &f.Lon, &f.peso); err != nil {
			return nil, nil, err
		}
		if f.Tipo == "Endereço" {
			i := strings.Index(f.Nome, ", ")
			if i <= 0 {
				continue
			}
			// Compara sem acento: a ordem dos ids é a do nome sem acento,
			// e a mesma rua aparece com maiúsculas diferentes.
			if rua := semAcentoRapido(f.Nome[:i]); rua != ruaAtual {
				fecharRua()
				ruaAtual = rua
			}
			contagem[cidadeDe(f.Detalhe)]++
			continue
		}
		// Linha de rua: a contagem da rua anterior acabou, salvo se o
		// nome dela continua a rua ("Rua X, Travessa" no meio de "Rua X, 12").
		norm := semAcentoRapido(f.Nome)
		if ruaAtual != "" && strings.HasPrefix(norm, ruaAtual+", ") {
			continue
		}
		fecharRua()
		ruaAtual = ""
		if len(pend) == 0 {
			continue
		}
		// Tira as ruas que já passaram na ordem.
		k := 0
		for _, p := range pend {
			if strings.HasPrefix(norm, p.rua) {
				pend[k] = p
				k++
			}
		}
		pend = pend[:k]
		for i := range pend {
			p := &pend[i]
			if p.rua == norm && p.cidade == cidadeDe(f.Detalhe) {
				// Todo trecho ganha o bônus; só um fica em memória, para
				// os prefixos curtos (os outros vêm do disco).
				fama[f.id] = bonusFama(p.n)
				if !p.emMemoria {
					p.emMemoria = true
					f.txt = prepararTexto(&f.Lugar)
					famosas = append(famosas, f)
				}
				break
			}
		}
	}
	return famosas, fama, rows.Err()
}

// cidadeDe tira a cidade do detalhe ("Tipo · Bairro, Cidade").
func cidadeDe(detalhe string) string {
	_, ctxto, _ := strings.Cut(detalhe, " · ")
	if i := strings.LastIndex(ctxto, ", "); i >= 0 {
		ctxto = ctxto[i+2:]
	}
	return ctxto
}

// publicar troca o estado só se o banco ainda for o mesmo.
func (b *Busca) publicar(db *sql.DB, est *indiceBusca) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.db == db {
		b.estado.Store(est)
		b.respMu.Lock()
		b.respostas, b.ordemResp = nil, nil
		b.respMu.Unlock()
	}
}

// idsOrdenados confere, por amostra, se o id segue a chave do gerador
// (nome sem acento + "|"). Se não seguir, a faixa de nome fica desligada.
func idsOrdenados(ctx context.Context, db *sql.DB, maxID int64) bool {
	if maxID < 2 {
		return maxID == 1
	}
	r := rand.New(rand.NewPCG(uint64(maxID), 7))
	for range 200 {
		id := 1 + r.Int64N(maxID-1)
		rows, err := db.QueryContext(ctx, `SELECT nome FROM lugares WHERE id >= ? ORDER BY id LIMIT 2`, id)
		if err != nil {
			return false
		}
		var nomes []string
		for rows.Next() {
			var n string
			if rows.Scan(&n) == nil {
				nomes = append(nomes, n)
			}
		}
		rows.Close()
		if len(nomes) == 2 && chaveNome(nomes[0]) > chaveNome(nomes[1]) {
			return false
		}
	}
	return true
}

// chaveNome repete o começo da chave do gerador (gerarbusca.go).
func chaveNome(nome string) string { return semAcentoRapido(nome) + "|" }

type Lugar struct {
	Nome    string  `json:"nome"`
	Detalhe string  `json:"detalhe"`
	Tipo    string  `json:"tipo"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	peso    float64
	nota    float64
}

func distanciaKm(lat1, lon1, lat2, lon2 float64) float64 {
	const r = 6371.0
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp, dl := (lat2-lat1)*math.Pi/180, (lon2-lon1)*math.Pi/180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(a)))
}

// palavras separa como o tokenizador unicode61 do FTS: letras e números.
func palavras(s string) []string {
	return strings.FieldsFunc(semAcento(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

// Palavras que quase todo endereço tem e não ajudam a achar.
var palavrasVazias = map[string]bool{"de": true, "da": true, "do": true, "dos": true, "das": true, "e": true}

// Abreviações comuns em endereço. A busca aceita as duas formas.
var abreviacoes = map[string]string{
	"av": "avenida", "r": "rua", "al": "alameda", "pca": "praca", "tv": "travessa",
	"trav": "travessa", "est": "estrada", "rod": "rodovia", "lgo": "largo",
	"dr": "doutor", "prof": "professor", "pres": "presidente", "cel": "coronel",
	"mal": "marechal", "gen": "general", "sen": "senador", "gov": "governador",
	"sta": "santa", "sto": "santo", "eng": "engenheiro", "dep": "deputado",
}

// termo é uma palavra da consulta. O último termo vale como prefixo,
// salvo se o texto terminar em espaço (palavra completa).
type termo struct {
	texto   string
	prefixo bool
	extenso string // forma por extenso da abreviação, se houver
	outro   string // outro gênero: "rodoviaria" acha "rodoviario"
}

type consulta struct {
	normal string  // texto sem acento, para a faixa de nome
	termos []termo // sem palavras vazias
	// temNumero: algum termo tem algarismo ("Av Paulista 1000").
	temNumero bool
}

func montarConsulta(texto string) consulta {
	c := consulta{normal: semAcento(texto)}
	ps := palavras(texto)
	fimAberto := texto != "" && !unicode.IsSpace(rune(texto[len(texto)-1]))
	for i, p := range ps {
		ultimo := i == len(ps)-1
		if palavrasVazias[p] && len(ps) > 1 && !ultimo {
			continue
		}
		t := termo{texto: p, prefixo: ultimo && fimAberto, extenso: abreviacoes[p]}
		if !t.prefixo && len(p) >= 5 {
			switch p[len(p)-1] {
			case 'a':
				t.outro = p[:len(p)-1] + "o"
			case 'o':
				t.outro = p[:len(p)-1] + "a"
			}
		}
		c.termos = append(c.termos, t)
		if strings.ContainsAny(p, "0123456789") {
			c.temNumero = true
		}
		if len(c.termos) == 8 {
			break
		}
	}
	return c
}

// prefixos devolve o texto sem acento e, se a primeira palavra for
// abreviação, também a forma por extenso ("av paulista" e
// "avenida paulista").
func (c consulta) prefixos() []string {
	ps := []string{c.normal}
	if t := c.termos[0]; t.extenso != "" && strings.HasPrefix(c.normal, t.texto+" ") {
		ps = append(ps, t.extenso+strings.TrimPrefix(c.normal, t.texto))
	}
	return ps
}

// fts monta a expressão MATCH. Termo de prefixo com uma letra fica de
// fora: sem índice de prefixo de 1 letra, o FTS juntaria milhões de
// termos. Ele é conferido depois, em nota.
func (c consulta) fts() string {
	var partes []string
	for _, t := range c.termos {
		var p string
		switch {
		case t.prefixo && len([]rune(t.texto)) < 2:
			if t.extenso == "" {
				continue
			}
			p = `"` + t.extenso + `"`
		case t.prefixo:
			p = `"` + t.texto + `"*`
		default:
			p = `"` + t.texto + `"`
		}
		if t.extenso != "" && !strings.Contains(p, t.extenso) && !(t.prefixo && strings.HasPrefix(t.extenso, t.texto)) {
			p = "(" + p + ` OR "` + t.extenso + `")`
		}
		partes = append(partes, p)
	}
	return strings.Join(partes, " AND ")
}

// achar diz se o termo aparece em normal (" palavra palavra ").
func (t termo) achar(normal string) bool {
	if t.prefixo {
		if strings.Contains(normal, " "+t.texto) {
			return true
		}
		return t.extenso != "" && strings.Contains(normal, " "+t.extenso+" ")
	}
	if strings.Contains(normal, " "+t.texto+" ") {
		return true
	}
	if t.outro != "" && strings.Contains(normal, " "+t.outro+" ") {
		return true
	}
	return t.extenso != "" && strings.Contains(normal, " "+t.extenso+" ")
}

// normalPalavras devolve " palavra palavra " sem acento. Texto só em
// ASCII (a maioria dos nomes) vai pelo caminho rápido, sem NFD.
func normalPalavras(s string) string {
	if !soASCII(s) {
		return " " + strings.Join(palavras(s), " ") + " "
	}
	b := make([]byte, 0, len(s)+2)
	b = append(b, ' ')
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
			b = append(b, ch+'a'-'A')
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			b = append(b, ch)
		default:
			if b[len(b)-1] != ' ' {
				b = append(b, ' ')
			}
		}
	}
	if b[len(b)-1] != ' ' {
		b = append(b, ' ')
	}
	return string(b)
}

func soASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// semAcentoRapido é semAcento com caminho rápido para ASCII.
func semAcentoRapido(s string) string {
	if !soASCII(s) {
		return semAcento(s)
	}
	b := make([]byte, 0, len(s))
	espaco := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch ch {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			espaco = true
			continue
		}
		if espaco && len(b) > 0 {
			b = append(b, ' ')
		}
		espaco = false
		if ch >= 'A' && ch <= 'Z' {
			ch += 'a' - 'A'
		}
		b = append(b, ch)
	}
	return string(b)
}

// nota pontua um lugar que tem todos os termos. Devolve false se faltar
// algum termo.
func (c consulta) nota(l *Lugar, x *textoLugar, lat, lon float64, temCentro bool) (float64, bool) {
	nome, tipo, bairro, cidade := x.nome, x.tipo, x.bairro, x.cidade
	n := l.peso
	for i, t := range c.termos {
		achou := false
		// Prefixo curto ("p") no tipo casaria "principal" em toda avenida.
		if (!t.prefixo || len(t.texto) >= 3) && t.achar(tipo) {
			n += 3 // pediu pela categoria: "rodoviária", "hospital"
			achou = true
		}
		switch {
		case t.achar(nome):
			n += 3
			if i == 0 && (strings.HasPrefix(nome, " "+t.texto) || (t.extenso != "" && strings.HasPrefix(nome, " "+t.extenso+" "))) {
				n += 3
			}
		case t.achar(cidade):
			n += 3
		case t.achar(bairro):
			n += 1
		default:
			if !achou {
				return 0, false
			}
		}
	}
	// Bônus para o nome que começa pelo texto todo. Só calcula o nome
	// sem acento quando o primeiro termo já abre o nome.
	if t := c.termos[0]; strings.HasPrefix(nome, " "+t.texto) || (t.extenso != "" && strings.HasPrefix(nome, " "+t.extenso)) {
		nn := semAcentoRapido(l.Nome)
		for _, p := range c.prefixos() {
			if nn == p {
				n += 6
				break
			} else if strings.HasPrefix(nn, p) {
				n += 3
				break
			}
		}
	}
	// Sem número na consulta, a rua vem antes dos endereços numerados
	// ("Rua X, 35") e dos pontos com nome de endereço (uma rodoviária
	// chamada "Rua Joaquim Machado, 35").
	if !c.temNumero && nomeComNumero(l.Nome) {
		n -= 5
	}
	if temCentro {
		n -= 2.2 * math.Log2(1+distanciaKm(lat, lon, l.Lat, l.Lon))
	}
	return n, true
}

// nomeComNumero diz se o nome tem ", " seguido de número: "Rua X, 35".
func nomeComNumero(nome string) bool {
	for {
		i := strings.Index(nome, ", ")
		if i < 0 {
			return false
		}
		nome = nome[i+2:]
		if nome != "" && nome[0] >= '0' && nome[0] <= '9' {
			return true
		}
	}
}

// Procurar devolve até limite lugares. lat/lon (opcionais) puxam para
// perto. parcial indica que alguma fonte parou no limite ou no tempo.
func (b *Busca) Procurar(ctx context.Context, texto string, lat, lon float64, temCentro bool, limite int) ([]Lugar, bool, error) {
	inicio := time.Now()
	db := b.abrir()
	if db == nil {
		return nil, false, os.ErrNotExist
	}
	c := montarConsulta(texto)
	if len(c.termos) == 0 {
		return nil, false, nil
	}
	chave := c.normal + "|" + strconv.FormatBool(texto != strings.TrimRight(texto, " "))
	if temCentro {
		chave += "|" + strconv.FormatFloat(lat, 'f', 2, 64) + "," + strconv.FormatFloat(lon, 'f', 2, 64)
	}
	if r, ok := b.lembrar(chave); ok {
		return r.lista, r.parcial, nil
	}
	est := b.estado.Load()
	if est == nil {
		est = &indiceBusca{}
	}

	// O orçamento conta desde a chegada do pedido. As fontes rodam em
	// paralelo, cada uma na sua conexão, e somam no mesmo mapa.
	tctx, cancelar := context.WithDeadline(ctx, inicio.Add(orcamentoBusca))
	defer cancelar()
	var (
		mu      sync.Mutex
		achados = map[int64]Lugar{}
		parcial bool
		falha   error
		fechado bool
	)
	somarTexto := func(id int64, l Lugar, x *textoLugar) {
		if x == nil {
			t := prepararTexto(&l)
			x = &t
		}
		n, ok := c.nota(&l, x, lat, lon, temCentro)
		if !ok {
			return
		}
		l.nota = n + est.fama[id]
		mu.Lock()
		if !fechado {
			if velho, ja := achados[id]; !ja || velho.nota < l.nota {
				achados[id] = l
			}
		}
		mu.Unlock()
	}
	somar := func(id int64, l Lugar) { somarTexto(id, l, nil) }
	terminar := func(nome string, ini time.Time, cheio bool, err error) {
		if medirEtapa != nil {
			medirEtapa(nome, time.Since(ini), err)
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err != nil && tctx.Err() != nil:
			parcial = true // tempo esgotado ou cliente foi embora
		case err != nil:
			falha = err
		case cheio:
			parcial = true
		}
	}
	var wg sync.WaitGroup
	rodar := func(nome string, f func() (bool, error)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ini := time.Now()
			cheio, err := f()
			terminar(nome, ini, cheio, err)
		}()
	}

	// 1. Lugares fortes em memória.
	rodar("memoria", func() (bool, error) {
		// Só memória: segue mesmo com o orçamento no fim, para a cidade
		// não sumir; para se o cliente desistir.
		if cand, ok := est.candidatosFortes(c); ok {
			for i, k := range cand {
				if i%8192 == 0 && ctx.Err() != nil {
					return false, ctx.Err()
				}
				f := &est.fortes[k]
				somarTexto(f.id, f.Lugar, &f.txt)
			}
			return false, nil
		}
		for i := range est.fortes {
			if i%8192 == 0 && ctx.Err() != nil {
				return false, ctx.Err()
			}
			f := &est.fortes[i]
			somarTexto(f.id, f.Lugar, &f.txt)
		}
		return false, nil
	})

	// 2. Faixa de nome: nomes que começam pelo texto e, se o primeiro
	// termo for abreviação, pela forma por extenso. Uma palavra curta
	// sozinha ("Ru", "Rua") casa milhões de linhas em ordem alfabética:
	// lê pouco da faixa e pula o FTS, que não ajudam.
	curta := len(c.termos) == 1 && len([]rune(c.termos[0].texto)) <= 3
	if est.ordenado && !strings.Contains(c.normal, "|") {
		lim := limiteFaixa
		if curta {
			lim = limiteFaixa / 6
		}
		for _, p := range c.prefixos() {
			rodar("faixa "+p, func() (bool, error) { return faixaNome(tctx, db, est.maxID, p, lim, somar) })
		}
	}

	// 3. FTS sem ordenar por rank.
	if q := c.fts(); q != "" && !curta {
		rodar("fts", func() (bool, error) { return lerFTS(tctx, db, q, somar) })
	}

	// Espera as fontes. Se o orçamento acabar, segue com o que já veio:
	// as fontes param sozinhas pelo contexto e o que chegar depois é
	// descartado.
	pronto := make(chan struct{})
	go func() { wg.Wait(); close(pronto) }()
	esgotou := false
	select {
	case <-pronto:
	case <-tctx.Done():
		select {
		case <-pronto:
		case <-time.After(folgaBusca):
		}
		esgotou = true
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	mu.Lock()
	fechado = true
	if esgotou {
		parcial = true
	}
	if falha != nil {
		mu.Unlock()
		return nil, false, falha
	}
	lista := make([]Lugar, 0, len(achados))
	for _, l := range achados {
		lista = append(lista, l)
	}
	if curta {
		parcial = true
	}
	parc := parcial
	mu.Unlock()

	sort.Slice(lista, func(i, j int) bool {
		if lista[i].nota != lista[j].nota {
			return lista[i].nota > lista[j].nota
		}
		return lista[i].Nome < lista[j].Nome
	})
	lista = juntarRepetidos(lista, limite)
	if tctx.Err() == nil {
		b.guardar(chave, respostaBusca{lista, parc})
	}
	return lista, parc, nil
}

// juntarRepetidos tira o mesmo lugar que aparece mais de uma vez: um rua
// gravada em trechos de tipos diferentes ("Rua" e "Avenida ou rua
// principal"). Mesmo nome, mesma cidade e perto: fica o de nota maior,
// que já vem antes na lista.
func juntarRepetidos(lista []Lugar, limite int) []Lugar {
	cidade := func(l *Lugar) string { return cidadeDe(l.Detalhe) }
	var out []Lugar
	for i := range lista {
		l := &lista[i]
		repetido := false
		for j := range out {
			o := &out[j]
			raio := 5.0
			if cidade(l) == "" {
				raio = 1
			}
			if semAcentoRapido(o.Nome) == semAcentoRapido(l.Nome) && cidade(o) == cidade(l) &&
				distanciaKm(o.Lat, o.Lon, l.Lat, l.Lon) < raio {
				repetido = true
				break
			}
		}
		if !repetido {
			out = append(out, *l)
			if len(out) == limite {
				break
			}
		}
	}
	return out
}

// faixaNome lê os lugares cujo nome sem acento começa por p. A faixa de
// id sai de duas buscas binárias. Devolve true se parou no limite.
func faixaNome(ctx context.Context, db *sql.DB, maxID int64, p string, limite int, somar func(int64, Lugar)) (bool, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	st, err := conn.PrepareContext(ctx, `SELECT nome FROM lugares WHERE id >= ? ORDER BY id LIMIT 1`)
	if err != nil {
		return false, err
	}
	defer st.Close()
	// primeiro devolve o menor id cuja chave não fica antes de alvo.
	primeiro := func(antes func(chave string) bool) (int64, error) {
		lo, hi := int64(1), maxID+1
		for lo < hi {
			meio := lo + (hi-lo)/2
			var nome string
			if err := st.QueryRowContext(ctx, meio).Scan(&nome); err != nil {
				return 0, err
			}
			if antes(chaveNome(nome)) {
				lo = meio + 1
			} else {
				hi = meio
			}
		}
		return lo, nil
	}
	ini, err := primeiro(func(k string) bool { return k < p })
	if err != nil {
		return false, err
	}
	fim, err := primeiro(func(k string) bool { return k < p || strings.HasPrefix(k, p) })
	if err != nil {
		return false, err
	}
	if fim <= ini {
		return false, nil
	}
	cheio := fim-ini > int64(limite)
	if cheio {
		fim = ini + int64(limite)
	}
	rows, err := conn.QueryContext(ctx, `SELECT id, nome, detalhe, tipo, lat, lon, peso FROM lugares WHERE id >= ? AND id < ?`, ini, fim)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var l Lugar
		if err := rows.Scan(&id, &l.Nome, &l.Detalhe, &l.Tipo, &l.Lat, &l.Lon, &l.peso); err != nil {
			return false, err
		}
		somar(id, l)
	}
	return cheio, rows.Err()
}

// lerFTS lê até limiteFTS lugares que casam com q, na ordem do id.
func lerFTS(ctx context.Context, db *sql.DB, q string, somar func(int64, Lugar)) (bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT l.id, l.nome, l.detalhe, l.tipo, l.lat, l.lon, l.peso
		FROM lugares_fts JOIN lugares l ON l.id = lugares_fts.rowid
		WHERE lugares_fts MATCH ? LIMIT ?`, q, limiteFTS+1)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		var l Lugar
		if err := rows.Scan(&id, &l.Nome, &l.Detalhe, &l.Tipo, &l.Lat, &l.Lon, &l.peso); err != nil {
			return false, err
		}
		n++
		if n > limiteFTS {
			return true, nil
		}
		somar(id, l)
	}
	return false, rows.Err()
}

func (b *Busca) lembrar(chave string) (respostaBusca, bool) {
	b.respMu.Lock()
	defer b.respMu.Unlock()
	r, ok := b.respostas[chave]
	return r, ok
}

func (b *Busca) guardar(chave string, r respostaBusca) {
	b.respMu.Lock()
	defer b.respMu.Unlock()
	if b.respostas == nil {
		b.respostas = map[string]respostaBusca{}
	}
	if _, ok := b.respostas[chave]; !ok {
		b.ordemResp = append(b.ordemResp, chave)
	}
	b.respostas[chave] = r
	for len(b.ordemResp) > maxRespostas {
		delete(b.respostas, b.ordemResp[0])
		b.ordemResp = b.ordemResp[1:]
	}
}

func (b *Busca) handler(w http.ResponseWriter, r *http.Request) {
	texto := strings.TrimLeft(r.URL.Query().Get("q"), " \t")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if len([]rune(strings.TrimSpace(texto))) < 2 {
		_, _ = w.Write([]byte(`{"lugares":[]}`))
		return
	}
	lat, e1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, e2 := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	lista, parcial, err := b.Procurar(r.Context(), texto, lat, lon, e1 == nil && e2 == nil, 12)
	if os.IsNotExist(err) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"erro":"Índice de busca ainda não instalado."}`))
		return
	}
	if r.Context().Err() != nil {
		return // o cliente desistiu; a consulta já parou
	}
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"erro": err.Error()})
		return
	}
	if lista == nil {
		lista = []Lugar{}
	}
	resp := map[string]any{"lugares": lista}
	if parcial {
		resp["parcial"] = true
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// handlerPronta diz se o índice existe, sem consultá-lo.
func (b *Busca) handlerPronta(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if !b.pronta() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"pronta":false}`))
		return
	}
	_, _ = w.Write([]byte(`{"pronta":true}`))
}
