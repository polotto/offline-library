package lab

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

// Processo mantém um programa de apoio (Kiwix, BRouter) rodando e o
// reinicia se cair. Os argumentos são recalculados a cada início.
type Processo struct {
	Nome    string
	Porta   int
	montar  func(porta int) (*exec.Cmd, error)
	logPath string

	mu       sync.Mutex
	cmd      *exec.Cmd
	estado   string
	detalhe  string
	reinicio chan struct{}
}

func NovoProcesso(nome, logs string, montar func(int) (*exec.Cmd, error)) *Processo {
	return &Processo{Nome: nome, montar: montar, logPath: filepath.Join(logs, nome+".log"),
		estado: "parado", detalhe: "Ainda não iniciado.", reinicio: make(chan struct{}, 1)}
}

func (p *Processo) Estado() (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.estado, p.detalhe
}

func (p *Processo) marcar(e, d string) {
	p.mu.Lock()
	p.estado, p.detalhe = e, d
	p.mu.Unlock()
}

// Reiniciar encerra o processo atual; o laço sobe outro com argumentos novos.
func (p *Processo) Reiniciar() {
	select {
	case p.reinicio <- struct{}{}:
	default:
	}
	p.mu.Lock()
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	p.mu.Unlock()
}

func portaLivre() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func esperarPorta(porta int, limite time.Duration) bool {
	fim := time.Now().Add(limite)
	for time.Now().Before(fim) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", porta), time.Second)
		if err == nil {
			c.Close()
			return true
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// Rodar mantém o processo até o contexto acabar.
func (p *Processo) Rodar(ctx context.Context) {
	espera := 2 * time.Second
	for ctx.Err() == nil {
		porta, err := portaLivre()
		if err != nil {
			p.marcar("falha", err.Error())
			time.Sleep(espera)
			continue
		}
		cmd, err := p.montar(porta)
		if err != nil {
			// Falta programa ou dado: não é falha, o serviço fica indisponível.
			p.marcar("parado", err.Error())
			select {
			case <-ctx.Done():
				return
			case <-p.reinicio:
			case <-time.After(time.Minute):
			}
			continue
		}
		logf, _ := os.OpenFile(p.logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		var saida io.Writer = io.Discard
		if logf != nil {
			saida = logf
		}
		cmd.Stdout, cmd.Stderr = saida, saida
		esconderJanela(cmd)
		inicio := time.Now()
		if err := cmd.Start(); err != nil {
			p.marcar("falha", "Não iniciou: "+err.Error())
			if logf != nil {
				logf.Close()
			}
			time.Sleep(espera)
			continue
		}
		p.mu.Lock()
		p.cmd, p.Porta = cmd, porta
		p.estado, p.detalhe = "iniciando", "Iniciando."
		p.mu.Unlock()
		log.Printf("%s: iniciado na porta interna %d (PID %d)", p.Nome, porta, cmd.Process.Pid)
		terminou := make(chan error, 1)
		go func() { terminou <- cmd.Wait() }()
		go func() {
			if esperarPorta(porta, 90*time.Second) {
				p.mu.Lock()
				if p.cmd == cmd {
					p.estado, p.detalhe = "pronto", "Em funcionamento."
				}
				p.mu.Unlock()
			}
		}()
		select {
		case <-ctx.Done():
			_ = cmd.Process.Kill()
			<-terminou
			if logf != nil {
				logf.Close()
			}
			return
		case err := <-terminou:
			if logf != nil {
				logf.Close()
			}
			p.mu.Lock()
			p.cmd = nil
			p.mu.Unlock()
			select {
			case <-p.reinicio:
				log.Printf("%s: reiniciando", p.Nome)
				espera = 2 * time.Second
				continue
			default:
			}
			log.Printf("%s: terminou (%v); nova tentativa em %s", p.Nome, err, espera)
			p.marcar("falha", fmt.Sprintf("Encerrou: %v. Nova tentativa em instantes.", err))
			if time.Since(inicio) > 5*time.Minute {
				espera = 2 * time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(espera):
			}
			if espera < 5*time.Minute {
				espera *= 2
			}
		}
	}
}

// Programa procura um executável de apoio na pasta de runtime da
// plataforma; sem ele, procura no sistema.
func (c *Config) Programa(caminho ...string) (string, error) {
	nome := caminho[len(caminho)-1]
	if runtime.GOOS == "windows" {
		caminho[len(caminho)-1] = nome + ".exe"
	}
	local := c.Caminho(append([]string{"runtime", Plataforma()}, caminho...)...)
	if st, err := os.Stat(local); err == nil && !st.IsDir() {
		return local, nil
	}
	if p, err := exec.LookPath(nome); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("programa %s ausente em runtime/%s", nome, Plataforma())
}

// Kiwix serve os ZIM conferidos (com marcador .ok) em /wiki.
func (c *Config) Kiwix() func(int) (*exec.Cmd, error) {
	return func(porta int) (*exec.Cmd, error) {
		prog, err := c.Programa("kiwix-serve")
		if err != nil {
			return nil, err
		}
		zims := ZimsProntos(c)
		if len(zims) == 0 {
			return nil, fmt.Errorf("nenhum pacote ZIM conferido em kiwix/")
		}
		args := []string{"--address=127.0.0.1", fmt.Sprintf("--port=%d", porta),
			"--urlRootLocation=/wiki", "--threads=2"}
		for _, z := range zims {
			args = append(args, z.Caminho)
		}
		return exec.Command(prog, args...), nil
	}
}

// Rotas sobe o BRouter com o Java da plataforma.
func (c *Config) Rotas() func(int) (*exec.Cmd, error) {
	return func(porta int) (*exec.Cmd, error) {
		dir := c.Caminho("mapas", "rotas")
		jar := filepath.Join(dir, "brouter.jar")
		for _, p := range []string{jar, filepath.Join(dir, "segments4"), filepath.Join(dir, "profiles2")} {
			if _, err := os.Stat(p); err != nil {
				return nil, fmt.Errorf("dados de rotas ausentes em mapas/rotas")
			}
		}
		java, err := c.Programa("jre", "bin", "java")
		if err != nil {
			return nil, fmt.Errorf("Java ausente em runtime/%s/jre", Plataforma())
		}
		_ = os.MkdirAll(filepath.Join(dir, "customprofiles"), 0o755)
		mem := c.MemoriaRotasMB
		if mem <= 0 {
			mem = 192
		}
		cmd := exec.Command(java, "-Xms32m", fmt.Sprintf("-Xmx%dm", mem), "-XX:ActiveProcessorCount=1",
			"-DmaxRunningTime=120", "-cp", jar, "btools.server.RouteServer",
			filepath.Join(dir, "segments4"), filepath.Join(dir, "profiles2"),
			filepath.Join(dir, "customprofiles"), fmt.Sprint(porta), "1", "127.0.0.1")
		cmd.Dir = dir
		return cmd, nil
	}
}
