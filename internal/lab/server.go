package lab

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/polotto/offline-library/web"
)

// Versao é preenchida na compilação.
var Versao = "desenvolvimento"

type Servidor struct {
	cfg         *Config
	kiwix       *Processo
	rotas       *Processo
	filaRotas   chan struct{}
	catalogo    *Catalogo
	busca       *Busca
	atualizador *Atualizador
	web         fs.FS

	estadoMu    sync.Mutex
	estadoEm    time.Time
	estadoCache []byte
}

func NovoServidor(cfg *Config) (*Servidor, error) {
	if err := os.MkdirAll(cfg.Logs, 0o755); err != nil {
		return nil, err
	}
	cat, err := AbrirCatalogo(cfg)
	if err != nil {
		return nil, fmt.Errorf("catálogo: %w", err)
	}
	s := &Servidor{cfg: cfg, catalogo: cat, web: web.Arquivos,
		kiwix:     NovoProcesso("kiwix", cfg.Logs, cfg.Kiwix()),
		rotas:     NovoProcesso("rotas", cfg.Logs, cfg.Rotas()),
		filaRotas: make(chan struct{}, 1),
		busca:     AbrirBusca(cfg),
	}
	s.atualizador = NovoAtualizador(cfg, s.kiwix, s.rotas)
	s.atualizador.antesDeTrocarBusca = s.busca.Fechar
	return s, nil
}

func (s *Servidor) Rotas() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /{$}", s.arquivoWeb("portal/index.html"))
	m.HandleFunc("GET /estado.json", s.estado)
	m.HandleFunc("GET /verificacao.json", s.verificar)
	m.HandleFunc("GET /mapas", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/mapas/", http.StatusMovedPermanently)
	})
	m.HandleFunc("GET /mapas/rotas", s.proxyRotas)
	m.HandleFunc("GET /mapas/busca", s.busca.handler)
	m.HandleFunc("GET /mapas/", s.mapas) // GET também atende HEAD
	m.Handle("/wiki/", s.proxyKiwix())
	m.HandleFunc("GET /wiki", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/wiki/", http.StatusMovedPermanently)
	})
	s.catalogo.Registrar(m)
	return s.filtro(s.registro(m))
}

// filtro aceita só a própria máquina e a rede local.
func (s *Servidor) filtro(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !s.cfg.Permitido(net.ParseIP(host)) {
			http.Error(w, "Acesso só pela rede local.", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		h.ServeHTTP(w, r)
	})
}

type gravador struct {
	http.ResponseWriter
	status int
}

func (g *gravador) WriteHeader(c int) { g.status = c; g.ResponseWriter.WriteHeader(c) }
func (g *gravador) Flush() {
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// registro grava pedidos com erro e pedidos lentos no log.
func (s *Servidor) registro(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inicio := time.Now()
		g := &gravador{ResponseWriter: w, status: 200}
		h.ServeHTTP(g, r)
		d := time.Since(inicio)
		if g.status >= 400 || d > 3*time.Second {
			log.Printf("http %d %s %s %s (%s)", g.status, r.Method, r.URL.Path, r.RemoteAddr, d.Round(time.Millisecond))
		}
	})
}

func (s *Servidor) arquivoWeb(nome string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.web, nome)
	}
}

// mapas serve a página e os recursos do repositório e os dados do disco.
func (s *Servidor) mapas(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(path.Clean(r.URL.Path), "/mapas")
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" {
		rel = "index.html"
	}
	if st, err := fs.Stat(s.web, "mapas/"+rel); err == nil && !st.IsDir() {
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.web, "mapas/"+rel)
		return
	}
	permitido := strings.HasSuffix(rel, ".pmtiles") ||
		rel == "style.json" || rel == "estilo-ruas.json" ||
		strings.HasPrefix(rel, "assets/")
	if !permitido || strings.Contains(rel, "/.") || strings.HasPrefix(rel, ".") {
		http.NotFound(w, r)
		return
	}
	arquivo := s.cfg.Caminho("mapas", filepath.FromSlash(rel))
	st, err := os.Stat(arquivo)
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(rel, ".pmtiles") || strings.HasSuffix(rel, ".pbf") {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	http.ServeFile(w, r, arquivo)
}

var perfis = map[string]bool{"car-fast": true, "trekking": true, "walking": true}

// proxyRotas encaminha ao BRouter. O BRouter roda com uma consulta por
// vez; a fila evita que uma consulta nova cancele a anterior.
func (s *Servidor) proxyRotas(w http.ResponseWriter, r *http.Request) {
	if !perfis[r.URL.Query().Get("profile")] {
		http.Error(w, "perfil inválido", http.StatusBadRequest)
		return
	}
	estado, detalhe := s.rotas.Estado()
	if estado != "pronto" {
		http.Error(w, "rotas indisponíveis: "+detalhe, http.StatusServiceUnavailable)
		return
	}
	select {
	case s.filaRotas <- struct{}{}:
		defer func() { <-s.filaRotas }()
	case <-time.After(60 * time.Second):
		http.Error(w, "rotas ocupadas, tente de novo", http.StatusServiceUnavailable)
		return
	case <-r.Context().Done():
		return
	}
	alvo := fmt.Sprintf("http://127.0.0.1:%d/brouter?%s", s.rotas.Porta, r.URL.RawQuery)
	ctx, cancel := context.WithTimeout(r.Context(), 130*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, alvo, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, "rotas indisponíveis: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for _, k := range []string{"Content-Type", "Content-Length"} {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = copiar(w, resp.Body)
}

func (s *Servidor) proxyKiwix() http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			u, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", s.kiwix.Porta))
			pr.SetURL(u)
			pr.Out.Host = pr.In.Host
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "Enciclopédia indisponível no momento.", http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if e, d := s.kiwix.Estado(); e != "pronto" {
			w.Header().Set("Retry-After", "10")
			http.Error(w, "Enciclopédia ainda não disponível: "+d, http.StatusServiceUnavailable)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

// Iniciar sobe os processos de apoio e o servidor web até o contexto acabar.
func (s *Servidor) Iniciar(ctx context.Context) error {
	go s.kiwix.Rodar(ctx)
	go s.vigiarColecoes(ctx)
	s.redirecionarPortasAntigas(ctx)
	go s.rotas.Rodar(ctx)
	if s.cfg.Atualizar {
		go s.atualizador.Rodar(ctx)
	}
	srv := &http.Server{
		Addr:              fmt.Sprintf("%s:%d", s.cfg.Endereco, s.cfg.Porta),
		Handler:           s.Rotas(),
		ReadHeaderTimeout: 20 * time.Second,
	}
	erro := make(chan error, 1)
	go func() { erro <- srv.ListenAndServe() }()
	log.Printf("Biblioteca no ar em http://%s (versão %s, %s, dados em %s)", Enderecos(s.cfg.Porta), Versao, Plataforma(), s.cfg.Dados)
	select {
	case err := <-erro:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		fim, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(fim)
		s.catalogo.Fechar()
		s.busca.Fechar()
		return nil
	}
}

// Enderecos lista os endereços de rede local para mostrar ao usuário.
func Enderecos(porta int) string {
	var lista []string
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && n.IP.IsPrivate() {
				lista = append(lista, comPorta(n.IP.String(), porta))
			}
		}
	}
	lista = append(lista, comPorta("127.0.0.1", porta))
	return strings.Join(lista, " e http://")
}

// comPorta junta host e porta e omite a 80, padrão do navegador.
func comPorta(host string, porta int) string {
	if porta == 80 {
		return host
	}
	return net.JoinHostPort(host, strconv.Itoa(porta))
}

// vigiarColecoes reinicia o Kiwix quando a lista de coleções conferidas
// muda, por exemplo quando outro programa termina um download.
func (s *Servidor) vigiarColecoes(ctx context.Context) {
	assinatura := func() string {
		var l []string
		for _, z := range ZimsProntos(s.cfg) {
			l = append(l, z.Arquivo)
		}
		return strings.Join(l, "|")
	}
	atual := assinatura()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Minute):
		}
		if nova := assinatura(); nova != atual {
			log.Printf("kiwix: coleções mudaram; reiniciando")
			atual = nova
			s.kiwix.Reiniciar()
		}
	}
}

func (s *Servidor) redirecionarPortasAntigas(ctx context.Context) {
	for porta, caminho := range s.cfg.PortasAntigas {
		caminho := strings.TrimSuffix("/"+strings.Trim(caminho, "/"), "/") + "/"
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host, _, err := net.SplitHostPort(r.Host)
			if err != nil {
				host = r.Host
			}
			destino := "http://" + comPorta(host, s.cfg.Porta) + caminho + strings.TrimPrefix(r.URL.Path, "/")
			if r.URL.RawQuery != "" {
				destino += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, destino, http.StatusMovedPermanently)
		})
		srv := &http.Server{Addr: s.cfg.Endereco + ":" + porta, Handler: s.filtro(h), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("porta antiga %s: %v", porta, err)
			}
		}()
		go func() { <-ctx.Done(); _ = srv.Close() }()
	}
}
