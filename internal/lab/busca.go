package lab

import (
	"database/sql"
	"encoding/json"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Busca consulta o índice de lugares em mapas/busca.db, gerado por
// "lab gerar-busca". Reabre o arquivo quando ele é trocado.
type Busca struct {
	caminho string
	mu      sync.Mutex
	db      *sql.DB
	versao  time.Time
}

func AbrirBusca(cfg *Config) *Busca {
	return &Busca{caminho: cfg.Caminho("mapas", "busca.db")}
}

func (b *Busca) abrir() *sql.DB {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, err := os.Stat(b.caminho)
	if err != nil {
		if b.db != nil {
			b.db.Close()
			b.db = nil
		}
		return nil
	}
	if b.db != nil && st.ModTime().Equal(b.versao) {
		return b.db
	}
	if b.db != nil {
		b.db.Close()
		b.db = nil
	}
	db, err := abrirSQLite(b.caminho, true)
	if err != nil {
		return nil
	}
	db.SetMaxOpenConns(2)
	b.db, b.versao = db, st.ModTime()
	return db
}

func (b *Busca) pronta() bool { return b.abrir() != nil }

func (b *Busca) Fechar() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.db != nil {
		b.db.Close()
		b.db = nil
	}
}

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

// Procurar devolve até limite lugares. lat/lon (opcionais) puxam para perto.
func (b *Busca) Procurar(texto string, lat, lon float64, temCentro bool, limite int) ([]Lugar, error) {
	db := b.abrir()
	if db == nil {
		return nil, os.ErrNotExist
	}
	q := consultaFTS(texto)
	if q == "" {
		return nil, nil
	}
	rows, err := db.Query(`SELECT l.nome, l.detalhe, l.tipo, l.lat, l.lon, l.peso, bm25(lugares_fts, 8.0, 1.0)
		FROM lugares_fts JOIN lugares l ON l.id = lugares_fts.rowid
		WHERE lugares_fts MATCH ? ORDER BY rank LIMIT 600`, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	primeiro := strings.Fields(semAcento(texto))
	var lista []Lugar
	for rows.Next() {
		var l Lugar
		var bm float64
		if err := rows.Scan(&l.Nome, &l.Detalhe, &l.Tipo, &l.Lat, &l.Lon, &l.peso, &bm); err != nil {
			return nil, err
		}
		nota := -bm + l.peso
		nome := semAcento(l.Nome)
		if len(primeiro) > 0 && strings.HasPrefix(nome, primeiro[0]) {
			nota += 4
		}
		if nome == semAcento(texto) {
			nota += 6
		}
		if temCentro {
			nota -= 2.2 * math.Log2(1+distanciaKm(lat, lon, l.Lat, l.Lon))
		}
		l.nota = nota
		lista = append(lista, l)
	}
	sort.SliceStable(lista, func(i, j int) bool { return lista[i].nota > lista[j].nota })
	if len(lista) > limite {
		lista = lista[:limite]
	}
	return lista, rows.Err()
}

func (b *Busca) handler(w http.ResponseWriter, r *http.Request) {
	texto := strings.TrimSpace(r.URL.Query().Get("q"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if len([]rune(texto)) < 2 {
		_, _ = w.Write([]byte(`{"lugares":[]}`))
		return
	}
	lat, e1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, e2 := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	lista, err := b.Procurar(texto, lat, lon, e1 == nil && e2 == nil, 12)
	if os.IsNotExist(err) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"erro":"Índice de busca ainda não instalado."}`))
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"erro": err.Error()})
		return
	}
	if lista == nil {
		lista = []Lugar{}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"lugares": lista})
}
