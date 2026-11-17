package lab

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/paulmach/osm"
	"github.com/paulmach/osm/osmpbf"
)

// O índice é gerado em duas leituras do arquivo .osm.pbf para caber em
// pouca memória: primeiro as vias (guardando só o nó do meio de cada
// uma), depois os nós (coordenadas, lugares, pontos e endereços).

var vias = map[string]bool{"motorway": true, "trunk": true, "primary": true, "secondary": true,
	"tertiary": true, "unclassified": true, "residential": true, "living_street": true,
	"pedestrian": true, "road": true}

var tiposLugar = map[string]struct {
	nome string
	peso float64
}{
	"city": {"Cidade", 9}, "town": {"Cidade", 7}, "village": {"Vila", 4}, "hamlet": {"Povoado", 2},
	"suburb": {"Bairro", 4}, "neighbourhood": {"Bairro", 3}, "quarter": {"Bairro", 3},
	"borough": {"Distrito", 4}, "island": {"Ilha", 3}, "locality": {"Localidade", 1},
}

var rotulosPonto = map[string]string{
	"pharmacy": "Farmácia", "hospital": "Hospital", "clinic": "Clínica", "doctors": "Consultório",
	"dentist": "Dentista", "school": "Escola", "university": "Universidade", "college": "Faculdade",
	"kindergarten": "Educação infantil", "library": "Biblioteca", "restaurant": "Restaurante",
	"fast_food": "Lanchonete", "cafe": "Café", "bar": "Bar", "pub": "Bar", "bank": "Banco",
	"atm": "Caixa eletrônico", "fuel": "Posto de combustível", "police": "Polícia",
	"fire_station": "Bombeiros", "post_office": "Correios", "place_of_worship": "Templo",
	"townhall": "Prefeitura", "courthouse": "Fórum", "marketplace": "Mercado público",
	"bus_station": "Rodoviária", "theatre": "Teatro", "cinema": "Cinema", "parking": "Estacionamento",
	"supermarket": "Supermercado", "bakery": "Padaria", "convenience": "Conveniência",
	"butcher": "Açougue", "hardware": "Ferragens", "clothes": "Roupas", "mall": "Shopping",
	"department_store": "Loja de departamentos", "car_repair": "Oficina", "hotel": "Hotel",
	"hostel": "Albergue", "guest_house": "Pousada", "museum": "Museu", "attraction": "Atração",
	"viewpoint": "Mirante", "camp_site": "Camping", "park": "Parque", "stadium": "Estádio",
	"sports_centre": "Centro esportivo", "playground": "Parquinho", "station": "Estação",
	"aerodrome": "Aeroporto", "monument": "Monumento", "memorial": "Memorial", "ruins": "Ruínas",
	"veterinary": "Veterinário", "community_centre": "Centro comunitário", "social_facility": "Assistência social",
}

func classificarPonto(t osm.Tags) (string, float64) {
	for _, k := range []string{"amenity", "shop", "tourism", "leisure", "healthcare", "historic", "railway", "aeroway"} {
		v := t.Find(k)
		if v == "" || v == "no" {
			continue
		}
		switch k {
		case "railway":
			if v != "station" && v != "halt" {
				continue
			}
		case "aeroway":
			if v != "aerodrome" {
				continue
			}
		case "leisure":
			if v != "park" && v != "stadium" && v != "sports_centre" && v != "playground" && v != "nature_reserve" {
				continue
			}
		}
		rotulo := rotulosPonto[v]
		if rotulo == "" {
			if k == "shop" {
				rotulo = "Loja"
			} else {
				rotulo = strings.ToUpper(v[:1]) + strings.ReplaceAll(v[1:], "_", " ")
			}
		}
		peso := 1.5
		switch v {
		case "aerodrome", "hospital", "university", "stadium", "bus_station", "mall", "station":
			peso = 3
		}
		return rotulo, peso
	}
	return "", 0
}

func limpo(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\t", " ", "\n", " ", "\r", " ").Replace(s))
}

type lugarOSM struct {
	nome, tipo string
	lat, lon   float64
	cidade     bool
}

// GerarBusca lê o arquivo .osm.pbf e escreve o índice em saida.
func GerarBusca(ctx context.Context, pbf, saida string) error {
	inicio := time.Now()
	tmp, err := os.MkdirTemp(filepath.Dir(saida), "busca-tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	// Ordenações grandes do SQLite vão para a pasta de trabalho, não para
	// /tmp, que em muitos Linux fica na memória e é pequeno.
	if os.Getenv("SQLITE_TMPDIR") == "" {
		os.Setenv("SQLITE_TMPDIR", tmp)
		defer os.Unsetenv("SQLITE_TMPDIR")
	}
	procs := max(1, runtime.GOMAXPROCS(0)-1)

	// 1. Vias com nome: grava tipo, nome e o nó do meio.
	log.Printf("busca: lendo vias de %s", pbf)
	f, err := os.Open(pbf)
	if err != nil {
		return err
	}
	viasArq, _ := os.Create(filepath.Join(tmp, "vias.tsv"))
	wv := bufio.NewWriterSize(viasArq, 1<<20)
	var queridos []int64
	sc := osmpbf.New(ctx, f, procs)
	sc.SkipNodes, sc.SkipRelations = true, true
	sc.FilterWay = func(w *osm.Way) bool {
		if w.Tags.Find("name") == "" || len(w.Nodes) == 0 {
			return false
		}
		if vias[w.Tags.Find("highway")] {
			return true
		}
		r, _ := classificarPonto(w.Tags)
		return r != ""
	}
	nVias := 0
	for sc.Scan() {
		w := sc.Object().(*osm.Way)
		tipo, peso := "Rua", 2.0
		if !vias[w.Tags.Find("highway")] {
			tipo, peso = classificarPonto(w.Tags)
		} else if hw := w.Tags.Find("highway"); hw == "motorway" || hw == "trunk" {
			tipo, peso = "Rodovia", 2.5
		} else if hw == "primary" || hw == "secondary" {
			tipo, peso = "Avenida ou rua principal", 2.5
		}
		no := int64(w.Nodes[len(w.Nodes)/2].ID)
		queridos = append(queridos, no)
		fmt.Fprintf(wv, "%s\t%s\t%g\t%d\n", tipo, limpo(w.Tags.Find("name")), peso, no)
		nVias++
	}
	if err := sc.Err(); err != nil {
		return err
	}
	sc.Close()
	f.Close()
	wv.Flush()
	viasArq.Close()
	sort.Slice(queridos, func(i, j int) bool { return queridos[i] < queridos[j] })
	queridos = compactar(queridos)
	log.Printf("busca: %d vias e pontos em vias, %d nós a localizar (%s)", nVias, len(queridos), time.Since(inicio).Round(time.Second))

	// 2. Nós: coordenadas dos nós queridos, lugares, pontos e endereços.
	coords := make([]float32, 2*len(queridos))
	for i := range coords {
		coords[i] = float32(math.NaN())
	}
	achar := func(id int64) int {
		i := sort.Search(len(queridos), func(i int) bool { return queridos[i] >= id })
		if i < len(queridos) && queridos[i] == id {
			return i
		}
		return -1
	}
	f, err = os.Open(pbf)
	if err != nil {
		return err
	}
	nosArq, _ := os.Create(filepath.Join(tmp, "nos.tsv"))
	wn := bufio.NewWriterSize(nosArq, 1<<20)
	var lugares []lugarOSM
	sc = osmpbf.New(ctx, f, procs)
	sc.SkipWays, sc.SkipRelations = true, true
	sc.FilterNode = func(n *osm.Node) bool {
		return len(n.Tags) > 0 || achar(int64(n.ID)) >= 0
	}
	nPontos := 0
	for sc.Scan() {
		n := sc.Object().(*osm.Node)
		if i := achar(int64(n.ID)); i >= 0 {
			coords[2*i], coords[2*i+1] = float32(n.Lat), float32(n.Lon)
		}
		if len(n.Tags) == 0 {
			continue
		}
		nome := limpo(n.Tags.Find("name"))
		if p, ok := tiposLugar[n.Tags.Find("place")]; ok && nome != "" {
			lugares = append(lugares, lugarOSM{nome, n.Tags.Find("place"), n.Lat, n.Lon,
				p.nome == "Cidade" || p.nome == "Vila"})
			fmt.Fprintf(wn, "%s\t%s\t%g\t%f\t%f\n", p.nome, nome, p.peso, n.Lat, n.Lon)
			nPontos++
			continue
		}
		if nome != "" {
			if r, peso := classificarPonto(n.Tags); r != "" {
				fmt.Fprintf(wn, "%s\t%s\t%g\t%f\t%f\n", r, nome, peso, n.Lat, n.Lon)
				nPontos++
				continue
			}
		}
		if rua, num := limpo(n.Tags.Find("addr:street")), limpo(n.Tags.Find("addr:housenumber")); rua != "" && num != "" {
			fmt.Fprintf(wn, "Endereço\t%s, %s\t1\t%f\t%f\n", rua, num, n.Lat, n.Lon)
			nPontos++
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	sc.Close()
	f.Close()
	wn.Flush()
	nosArq.Close()
	log.Printf("busca: %d lugares e pontos em nós, %d nomes de localidades (%s)", nPontos, len(lugares), time.Since(inicio).Round(time.Second))

	// 3. Grade de localidades para achar cidade e bairro de cada ponto.
	grade := map[[2]int][]int{}
	celula := func(lat, lon float64) [2]int { return [2]int{int(math.Floor(lat * 4)), int(math.Floor(lon * 4))} }
	for i, l := range lugares {
		k := celula(l.lat, l.lon)
		grade[k] = append(grade[k], i)
	}
	contexto := func(lat, lon float64, ignorar string) string {
		k := celula(lat, lon)
		var cidade, bairro string
		dc, db := 25.0, 4.0
		for a := -1; a <= 1; a++ {
			for b := -1; b <= 1; b++ {
				for _, i := range grade[[2]int{k[0] + a, k[1] + b}] {
					l := lugares[i]
					d := distanciaKm(lat, lon, l.lat, l.lon)
					if l.cidade && d < dc && l.nome != ignorar {
						cidade, dc = l.nome, d
					} else if !l.cidade && d < db && l.nome != ignorar {
						bairro, db = l.nome, d
					}
				}
			}
		}
		switch {
		case bairro != "" && cidade != "":
			return bairro + ", " + cidade
		case cidade != "":
			return cidade
		default:
			return bairro
		}
	}

	// 4. Banco: grava tudo sem índice e depois tira as repetições (a mesma
	// rua em cada trecho) por ordenação. Índice único durante a gravação
	// espalha escritas pelo arquivo e fica lento com pouca memória.
	parcial := saida + ".gerando"
	os.Remove(parcial)
	db, err := abrirSQLite(parcial, false)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, q := range []string{
		`PRAGMA journal_mode=OFF`, `PRAGMA synchronous=OFF`, `PRAGMA cache_size=-32768`, `PRAGMA temp_store=FILE`,
		`CREATE TABLE bruto(nome TEXT, detalhe TEXT, tipo TEXT, lat REAL, lon REAL, peso REAL, chave TEXT)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	tx, _ := db.Begin()
	ins, err := tx.Prepare(`INSERT INTO bruto VALUES(?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	total := 0
	gravar := func(tipo, nome string, peso, lat, lon float64) error {
		ctxto := contexto(lat, lon, nome)
		detalhe := tipo
		if ctxto != "" {
			detalhe += " · " + ctxto
		}
		chave := semAcento(nome) + "|" + tipo + "|" + ctxto
		if tipo != "Rua" && tipo != "Rodovia" && tipo != "Avenida ou rua principal" && tipo != "Endereço" {
			chave += fmt.Sprintf("|%.3f|%.3f", lat, lon)
		}
		if _, err := ins.Exec(nome, detalhe, tipo, lat, lon, peso, chave); err != nil {
			return err
		}
		total++
		if total%10000 == 0 && ctx.Err() != nil {
			return ctx.Err()
		}
		if total%200000 == 0 {
			if err := tx.Commit(); err != nil {
				return err
			}
			tx, _ = db.Begin()
			ins, _ = tx.Prepare(`INSERT INTO bruto VALUES(?,?,?,?,?,?,?)`)
			log.Printf("busca: %d registros", total)
		}
		return nil
	}
	ler := func(nome string, fn func([]string) error) error {
		a, err := os.Open(filepath.Join(tmp, nome))
		if err != nil {
			return err
		}
		defer a.Close()
		s := bufio.NewScanner(a)
		s.Buffer(make([]byte, 1<<20), 1<<20)
		for s.Scan() {
			if err := fn(strings.Split(s.Text(), "\t")); err != nil {
				return err
			}
		}
		return s.Err()
	}
	if err := ler("nos.tsv", func(c []string) error {
		peso, _ := strconv.ParseFloat(c[2], 64)
		lat, _ := strconv.ParseFloat(c[3], 64)
		lon, _ := strconv.ParseFloat(c[4], 64)
		return gravar(c[0], c[1], peso, lat, lon)
	}); err != nil {
		return err
	}
	if err := ler("vias.tsv", func(c []string) error {
		peso, _ := strconv.ParseFloat(c[2], 64)
		id, _ := strconv.ParseInt(c[3], 10, 64)
		i := achar(id)
		if i < 0 || math.IsNaN(float64(coords[2*i])) {
			return nil
		}
		return gravar(c[0], c[1], peso, float64(coords[2*i]), float64(coords[2*i+1]))
	}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Printf("busca: %d registros gravados; tirando repetições", total)
	for _, q := range []string{
		`CREATE TABLE lugares(id INTEGER PRIMARY KEY, nome TEXT, detalhe TEXT, tipo TEXT, lat REAL, lon REAL, peso REAL)`,
		// Com max(), as outras colunas vêm da mesma linha do maior peso.
		`INSERT INTO lugares(nome, detalhe, tipo, lat, lon, peso)
		   SELECT nome, detalhe, tipo, lat, lon, max(peso) FROM bruto GROUP BY chave`,
		`DROP TABLE bruto`,
		`CREATE VIRTUAL TABLE lugares_fts USING fts5(nome, detalhe, content='lugares', content_rowid='id',
		   tokenize='unicode61 remove_diacritics 2', prefix='2 3')`,
		`INSERT INTO lugares_fts(lugares_fts) VALUES('rebuild')`,
		`INSERT INTO lugares_fts(lugares_fts) VALUES('optimize')`,
	} {
		if _, err := db.Exec(q); err != nil {
			return fmt.Errorf("%s: %w", strings.Fields(q)[0], err)
		}
	}
	// Cópia compacta direto no destino: precisa de espaço só para ela, sem
	// o diário de um VACUUM comum.
	compacto := saida + ".compacto"
	os.Remove(compacto)
	if _, err := db.Exec(`VACUUM INTO ?`, compacto); err != nil {
		return fmt.Errorf("VACUUM: %w", err)
	}
	db.Close()
	os.Remove(parcial)
	if err := os.Rename(compacto, saida); err != nil {
		return err
	}
	st, _ := os.Stat(saida)
	var unicos int
	if db2, err := abrirSQLite(saida, true); err == nil {
		_ = db2.QueryRow(`SELECT count(*) FROM lugares`).Scan(&unicos)
		db2.Close()
	}
	log.Printf("busca: pronto, %s com %d lugares de %d registros (%s)", tamanhoLegivel(st.Size()), unicos, total, time.Since(inicio).Round(time.Second))
	return nil
}

func compactar(l []int64) []int64 {
	if len(l) == 0 {
		return l
	}
	j := 0
	for i := 1; i < len(l); i++ {
		if l[i] != l[j] {
			j++
			l[j] = l[i]
		}
	}
	return l[:j+1]
}
