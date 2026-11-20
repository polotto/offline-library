package lab

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	orcamentoBusca = 250 * time.Millisecond // tempo máximo de uma busca
	limiteFaixa    = 4000                   // linhas lidas por faixa de nome
	limiteFTS      = 1500                   // linhas lidas por consulta FTS
	pesoForte      = 3.0                    // peso mínimo para ficar em memória
	maxRespostas   = 256                    // respostas guardadas
)

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
	novo := *est
	novo.fortes = fortes
	b.publicar(db, &novo)
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
func chaveNome(nome string) string { return semAcento(nome) + "|" }

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

func normalPalavras(s string) string { return " " + strings.Join(palavras(s), " ") + " " }

// nota pontua um lugar que tem todos os termos. Devolve false se faltar
// algum termo.
func (c consulta) nota(l *Lugar, x *textoLugar, lat, lon float64, temCentro bool) (float64, bool) {
	nome, tipo, bairro, cidade := x.nome, x.tipo, x.bairro, x.cidade
	n := l.peso
	for i, t := range c.termos {
		achou := false
		if t.achar(tipo) {
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
		nn := semAcento(l.Nome)
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
	if temCentro {
		n -= 2.2 * math.Log2(1+distanciaKm(lat, lon, l.Lat, l.Lon))
	}
	return n, true
}

// Procurar devolve até limite lugares. lat/lon (opcionais) puxam para
// perto. parcial indica que alguma fonte parou no limite ou no tempo.
func (b *Busca) Procurar(ctx context.Context, texto string, lat, lon float64, temCentro bool, limite int) ([]Lugar, bool, error) {
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

	achados := map[int64]Lugar{}
	parcial := false
	somarTexto := func(id int64, l Lugar, x *textoLugar) {
		if _, ja := achados[id]; ja {
			return
		}
		if x == nil {
			t := prepararTexto(&l)
			x = &t
		}
		if n, ok := c.nota(&l, x, lat, lon, temCentro); ok {
			l.nota = n
			achados[id] = l
		}
	}
	somar := func(id int64, l Lugar) { somarTexto(id, l, nil) }

	// 1. Lugares fortes em memória.
	for i := range est.fortes {
		if i%8192 == 0 && ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		f := &est.fortes[i]
		somarTexto(f.id, f.Lugar, &f.txt)
	}

	tctx, cancelar := context.WithTimeout(ctx, orcamentoBusca)
	defer cancelar()
	tempoEsgotado := func(err error) bool {
		if ctx.Err() != nil {
			return false
		}
		return tctx.Err() != nil || errors.Is(err, context.DeadlineExceeded)
	}

	// 2. Faixa de nome: nomes que começam pelo texto e, se o primeiro
	// termo for abreviação, pela forma por extenso.
	// Uma palavra curta sozinha ("Ru", "Rua") casa milhões de linhas em
	// ordem alfabética: lê pouco da faixa e pula o FTS, que não ajudam.
	curta := len(c.termos) == 1 && len([]rune(c.termos[0].texto)) <= 3
	if est.ordenado && !strings.Contains(c.normal, "|") {
		limite := limiteFaixa
		if curta {
			limite = limiteFaixa / 6
		}
		for _, p := range c.prefixos() {
			cheio, err := faixaNome(tctx, db, est.maxID, p, limite, somar)
			if err != nil {
				if tempoEsgotado(err) {
					parcial = true
					break
				}
				return nil, false, err
			}
			parcial = parcial || cheio
		}
	}

	// 3. FTS sem ordenar por rank.
	if q := c.fts(); q != "" && !curta && tctx.Err() == nil {
		cheio, err := lerFTS(tctx, db, q, somar)
		if err != nil {
			if !tempoEsgotado(err) {
				if ctx.Err() != nil {
					return nil, false, ctx.Err()
				}
				return nil, false, err
			}
			parcial = true
		}
		parcial = parcial || cheio
	}
	if curta {
		parcial = true
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}

	lista := make([]Lugar, 0, len(achados))
	for _, l := range achados {
		lista = append(lista, l)
	}
	sort.Slice(lista, func(i, j int) bool {
		if lista[i].nota != lista[j].nota {
			return lista[i].nota > lista[j].nota
		}
		return lista[i].Nome < lista[j].Nome
	})
	lista = juntarRepetidos(lista, limite)
	if tctx.Err() == nil {
		b.guardar(chave, respostaBusca{lista, parcial})
	}
	return lista, parcial, nil
}

// juntarRepetidos tira o mesmo lugar que aparece mais de uma vez: um rua
// gravada em trechos de tipos diferentes ("Rua" e "Avenida ou rua
// principal"). Mesmo nome, mesma cidade e perto: fica o de nota maior,
// que já vem antes na lista.
func juntarRepetidos(lista []Lugar, limite int) []Lugar {
	cidade := func(l *Lugar) string {
		_, ctxto, _ := strings.Cut(l.Detalhe, " · ")
		if i := strings.LastIndex(ctxto, ", "); i >= 0 {
			ctxto = ctxto[i+2:]
		}
		return ctxto
	}
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
			if semAcento(o.Nome) == semAcento(l.Nome) && cidade(o) == cidade(l) &&
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
