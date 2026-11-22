package lab

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

func TestComPorta(t *testing.T) {
	casos := map[string]string{
		comPorta("192.168.0.5", 80):   "192.168.0.5",
		comPorta("192.168.0.5", 8087): "192.168.0.5:8087",
		comPorta("lab-0.local", 80):   "lab-0.local",
	}
	for obtido, esperado := range casos {
		if obtido != esperado {
			t.Errorf("obtido %q, esperado %q", obtido, esperado)
		}
	}
}

func TestPermitidoForaDaRede(t *testing.T) {
	c := &Config{}
	if c.Permitido(net.ParseIP("8.8.8.8")) {
		t.Error("endereço de fora da rede aceito")
	}
	if !c.Permitido(net.ParseIP("192.168.0.9")) {
		t.Error("endereço privado recusado")
	}
}

func TestRedirecionarJellyfin(t *testing.T) {
	s := &Servidor{cfg: &Config{PortaJellyfin: 8096}}
	casos := map[string]string{
		"http://lab-0.local/jellyfin":       "http://lab-0.local:8096/",
		"http://192.168.0.5:8087/jellyfin/": "http://192.168.0.5:8096/",
	}
	for origem, esperado := range casos {
		req := httptest.NewRequest(http.MethodGet, origem, nil)
		rec := httptest.NewRecorder()
		s.redirecionarJellyfin(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("%s: status = %d, esperado %d", origem, rec.Code, http.StatusFound)
		}
		if got := rec.Header().Get("Location"); got != esperado {
			t.Errorf("%s: Location = %q, esperado %q", origem, got, esperado)
		}
	}
}

func TestEstadoJellyfinDisponivel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Healthy"))
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, portaTexto, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	porta, err := strconv.Atoi(portaTexto)
	if err != nil {
		t.Fatal(err)
	}

	s := &Servidor{cfg: &Config{PortaJellyfin: porta}}
	est := s.estadoJellyfin()
	if est.Estado != "pronto" {
		t.Fatalf("estado = %q (%s), esperado pronto", est.Estado, est.Detalhe)
	}
	if est.Porta != porta {
		t.Fatalf("porta = %d, esperado %d", est.Porta, porta)
	}
}
