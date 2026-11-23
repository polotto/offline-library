package lab

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Partes do buscador da página inicial que precisam ser rápidas num
// computador fraco (Atom, 2 GB, acervo em disco USB):
//
//   - o Kiwix roda com 2 linhas de execução e continua trabalhando num
//     pedido mesmo depois de o cliente desistir. Mandar 10 pedidos por
//     tecla enfileirava tudo e a busca passava de 20 s. Agora há no máximo
//     2 pedidos nossos por vez, em ordem de prioridade (português primeiro),
//     e um prazo; o que chegar até o prazo é mostrado.
//   - a busca de lugares do mapa ordena por rank (bm25), que custa 2 a 4 s
//     em termos comuns como "curitiba" (130 mil linhas). Aqui não há rank:
//     prefixo do nome por busca binária (os ids estão em ordem de nome) e
//     FTS sem ordenação, com a nota calculada em Go.
//   - resultados completos ficam 10 minutos num cache pequeno.

// vagasKiwix limita os pedidos simultâneos do buscador ao Kiwix.
var vagasKiwix = make(chan struct{}, 2)

func comVagaKiwix[T any](ctx context.Context, fn func() T) (T, bool) {
	var zero T
	select {
	case vagasKiwix <- struct{}{}:
	case <-ctx.Done():
		return zero, false
	}
	defer func() { <-vagasKiwix }()
	if ctx.Err() != nil {
		return zero, false
	}
	return fn(), true
}

// --- Cache curto de respostas

type entradaCache struct {
	quando time.Time
	valor  any
}

type cacheBusca struct {
	mu    sync.Mutex
	max   int
	ttl   time.Duration
	itens map[string]entradaCache
	ordem []string
}

var cacheBuscador = &cacheBusca{max: 128, ttl: 10 * time.Minute, itens: map[string]entradaCache{}}

func (c *cacheBusca) pegar(chave string) (any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.itens[chave]
	if !ok || time.Since(e.quando) > c.ttl {
		return nil, false
	}
	return e.valor, true
}

func (c *cacheBusca) guardar(chave string, valor any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.itens[chave]; !ok {
		c.ordem = append(c.ordem, chave)
	}
	c.itens[chave] = entradaCache{quando: time.Now(), valor: valor}
	for len(c.ordem) > c.max {
		delete(c.itens, c.ordem[0])
		c.ordem = c.ordem[1:]
	}
}

// --- Enciclopédia e dicionários

type tarefaWiki struct {
	ordem int
	fazer func(context.Context) []ResultadoWiki
}

// buscarWiki devolve os resultados e se todas as consultas terminaram
// (só resultado completo vai para o cache).
func (s *Servidor) buscarWiki(ctx context.Context, termo string, limite int) ([]ResultadoWiki, bool) {
	if est, _ := s.kiwix.Estado(); est != "pronto" {
		return nil, false
	}
	zims := ZimsProntos(s.cfg)
	if len(zims) == 0 {
		return nil, true
	}
	peso := func(z Zim) int {
		p := 0
		if !strings.Contains(z.Prefixo, "_pt_") {
			p += 2
		}
		if !strings.HasPrefix(z.Prefixo, "wikipedia_") {
			p++
		}
		return p
	}
	sort.SliceStable(zims, func(i, j int) bool {
		if pi, pj := peso(zims[i]), peso(zims[j]); pi != pj {
			return pi < pj
		}
		return zims[i].Prefixo < zims[j].Prefixo
	})

	// Ordem de prioridade: título exato e texto na Wikipédia em português,
	// depois o texto nos demais acervos em português, depois os outros idiomas.
	var tarefas []tarefaWiki
	for i, z := range zims {
		acervo := strings.TrimSuffix(z.Arquivo, ".zim")
		col := TituloColecao(z.Prefixo)
		pt := strings.Contains(z.Prefixo, "_pt_")
		if i == 0 && pt {
			tarefas = append(tarefas, tarefaWiki{fazer: func(c context.Context) []ResultadoWiki {
				return s.sugerirKiwix(c, acervo, col, termo, 2)
			}})
		}
		n := 2
		if pt {
			n = 4
		}
		tarefas = append(tarefas, tarefaWiki{fazer: func(c context.Context) []ResultadoWiki {
			return s.pesquisarKiwixHTML(c, acervo, col, termo, n)
		}})
	}
	for i := range tarefas {
		tarefas[i].ordem = i
	}

	fila := make(chan tarefaWiki, len(tarefas))
	for _, t := range tarefas {
		fila <- t
	}
	close(fila)
	lotes := make([][]ResultadoWiki, len(tarefas))
	feitas := make([]bool, len(tarefas))
	var wg sync.WaitGroup
	for w := 0; w < cap(vagasKiwix); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range fila {
				res, ok := comVagaKiwix(ctx, func() []ResultadoWiki { return t.fazer(ctx) })
				if !ok || ctx.Err() != nil {
					continue
				}
				lotes[t.ordem], feitas[t.ordem] = res, true
			}
		}()
	}
	wg.Wait()

	completo := true
	porURL := map[string]int{}
	var saida []ResultadoWiki
	for i, l := range lotes {
		completo = completo && feitas[i]
		for _, item := range l {
			if pos, ja := porURL[item.URL]; ja {
				if saida[pos].Trecho == "" && item.Trecho != "" {
					saida[pos].Trecho = item.Trecho
				}
				continue
			}
			porURL[item.URL] = len(saida)
			saida = append(saida, item)
		}
	}
	if len(saida) > limite {
		saida = saida[:limite]
	}
	return saida, completo
}

// --- Endereços e lugares

// ProcurarRapido acha lugares sem ordenar por rank no SQLite. Junta o bloco
// de nomes que começam pelo texto (busca binária nos ids, que seguem a ordem
// de semAcento(nome) desde o gerar-busca) com uma amostra do FTS.
func (b *Busca) ProcurarRapido(ctx context.Context, texto string, limite int) ([]Lugar, error) {
	db := b.abrir()
	if db == nil {
		return nil, nil
	}
	chave := semAcento(texto)
	q := consultaFTS(texto)
	if q == "" || chave == "" {
		return nil, nil
	}
	palavras := strings.FieldsFunc(chave, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})

	porID := map[int64]Lugar{}
	ler := func(consulta string, args ...any) error {
		rows, err := db.QueryContext(ctx, consulta, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var l Lugar
			if err := rows.Scan(&id, &l.Nome, &l.Detalhe, &l.Tipo, &l.Lat, &l.Lon, &l.peso); err != nil {
				return err
			}
			porID[id] = l
		}
		return rows.Err()
	}

	// 1. Nomes que começam pelo texto.
	var maxID int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(max(id), 0) FROM lugares`).Scan(&maxID); err != nil {
		return nil, err
	}
	lo, hi := int64(1), maxID+1
	for lo < hi {
		meio := lo + (hi-lo)/2
		var nome string
		err := db.QueryRowContext(ctx, `SELECT nome FROM lugares WHERE id >= ? ORDER BY id LIMIT 1`, meio).Scan(&nome)
		if err != nil {
			return nil, err
		}
		if semAcento(nome)+"|" >= chave {
			hi = meio
		} else {
			lo = meio + 1
		}
	}
	rows, err := db.QueryContext(ctx, `SELECT id, nome, detalhe, tipo, lat, lon, peso FROM lugares WHERE id >= ? ORDER BY id LIMIT 3000`, lo)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var l Lugar
		if err := rows.Scan(&id, &l.Nome, &l.Detalhe, &l.Tipo, &l.Lat, &l.Lon, &l.peso); err != nil {
			rows.Close()
			return nil, err
		}
		if !strings.HasPrefix(semAcento(l.Nome), chave) {
			break
		}
		porID[id] = l
	}
	rows.Close()

	// 2. Palavras em qualquer parte do nome ou do detalhe, sem rank.
	if err := ler(`SELECT l.id, l.nome, l.detalhe, l.tipo, l.lat, l.lon, l.peso
		FROM lugares_fts JOIN lugares l ON l.id = lugares_fts.rowid
		WHERE lugares_fts MATCH ? LIMIT 800`, q); err != nil && ctx.Err() == nil {
		return nil, err
	}

	lista := make([]Lugar, 0, len(porID))
	for _, l := range porID {
		nome := semAcento(l.Nome)
		nota := l.peso
		switch {
		case nome == chave:
			nota += 10
		case strings.HasPrefix(nome, chave):
			nota += 6
		case len(palavras) > 0 && strings.HasPrefix(nome, palavras[0]):
			nota += 4
		}
		todas := true
		for _, p := range palavras {
			if !strings.Contains(nome, p) {
				todas = false
				break
			}
		}
		if todas {
			nota += 2
		}
		l.nota = nota
		lista = append(lista, l)
	}
	sort.SliceStable(lista, func(i, j int) bool {
		if lista[i].nota != lista[j].nota {
			return lista[i].nota > lista[j].nota
		}
		return lista[i].Nome < lista[j].Nome
	})
	if len(lista) > limite {
		lista = lista[:limite]
	}
	return lista, nil
}

func (s *Servidor) buscarEnderecosRapido(ctx context.Context, termo string, limite int) []ResultadoEndereco {
	candidatos, err := s.busca.ProcurarRapido(ctx, termo, limite*4)
	if err != nil {
		return nil
	}
	var saida []ResultadoEndereco
	for _, l := range candidatos {
		duplicado := false
		for _, ex := range saida {
			if ex.Nome == l.Nome && ex.Tipo == l.Tipo && distanciaKm(ex.Lat, ex.Lon, l.Lat, l.Lon) < 1.5 {
				duplicado = true
				break
			}
		}
		if duplicado {
			continue
		}
		params := url.Values{
			"lat":  {strconv.FormatFloat(l.Lat, 'f', 6, 64)},
			"lon":  {strconv.FormatFloat(l.Lon, 'f', 6, 64)},
			"nome": {l.Nome},
			"tipo": {l.Tipo},
		}
		saida = append(saida, ResultadoEndereco{
			Nome: l.Nome, Detalhe: l.Detalhe, Tipo: l.Tipo, Lat: l.Lat, Lon: l.Lon,
			URL: "/mapas/?" + params.Encode(),
		})
		if len(saida) >= limite {
			break
		}
	}
	return saida
}
