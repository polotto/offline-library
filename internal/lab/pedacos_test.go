package lab

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBaixarPorPedacosRefazSoOQueEstragou(t *testing.T) {
	const tam = 1 << 16
	dados := make([]byte, 10*tam+123)
	_, _ = rand.Read(dados)
	var hashes []string
	for i := 0; i < len(dados); i += tam {
		h := sha1.Sum(dados[i:min(i+tam, len(dados))])
		hashes = append(hashes, hex.EncodeToString(h[:]))
	}
	total := sha256.Sum256(dados)
	pedidos := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos++
		http.ServeContent(w, r, "x", time.Time{}, bytes.NewReader(dados))
	}))
	defer srv.Close()

	// Parcial como o do aria2: começo certo, um buraco e um pedaço corrompido.
	parcial := filepath.Join(t.TempDir(), "x.lab-parcial")
	p := make([]byte, len(dados))
	copy(p, dados[:6*tam])
	p[2*tam+5] ^= 0xff
	if err := os.WriteFile(parcial, p[:7*tam], 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Atualizador{http: srv.Client(), baixando: map[string]*andamento{}}
	err := a.baixarPorPedacos(context.Background(), srv.URL, parcial, "x", int64(len(dados)), hex.EncodeToString(total[:]), tam, hashes)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(parcial)
	if !bytes.Equal(b, dados) {
		t.Fatal("arquivo final diferente da origem")
	}
	// Pedaço 2 sozinho e pedaços 6 a 10 juntos: dois pedidos.
	if pedidos != 2 {
		t.Fatalf("esperava 2 pedidos com faixa de bytes, vieram %d", pedidos)
	}
}
