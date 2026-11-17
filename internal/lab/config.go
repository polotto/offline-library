// Package lab implementa o servidor da biblioteca offline.
package lab

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
)

// Config reúne o que muda de uma instalação para outra. Fica em
// DADOS/config/lab.json, fora do repositório.
type Config struct {
	Nome            string   `json:"nome"`
	NomeCatalogo    string   `json:"nome_catalogo"`
	Endereco        string   `json:"endereco"`
	Porta           int      `json:"porta"`
	RedesPermitidas []string `json:"redes_permitidas"`
	Atualizar       bool     `json:"atualizar"`
	HoraAtualizacao int      `json:"hora_atualizacao"`
	MemoriaRotasMB  int      `json:"memoria_rotas_mb"`
	// PortasAntigas redireciona portas de instalações anteriores para um
	// caminho, por exemplo {"8088": "/wiki/"}, para manter favoritos.
	PortasAntigas map[string]string `json:"portas_antigas"`
	// PastaTrabalho recebe os arquivos grandes e temporários da busca de
	// endereços. Num disco USB instável, use uma pasta do disco interno.
	PastaTrabalho string `json:"pasta_trabalho"`
	// OSM é o arquivo .osm.pbf da região da busca de endereços.
	OSM string `json:"osm"`

	Dados string `json:"-"`
	Logs  string `json:"-"`
	redes []*net.IPNet
}

func padrao() Config {
	return Config{
		Nome:            "Biblioteca da casa",
		NomeCatalogo:    "Biblioteca de arquivos",
		Endereco:        "0.0.0.0",
		Porta:           8087,
		Atualizar:       true,
		HoraAtualizacao: 3,
		MemoriaRotasMB:  192,
	}
}

// Carregar lê lab.json se existir. Valores ausentes ficam no padrão.
func Carregar(dados string) (Config, error) {
	c := padrao()
	abs, err := filepath.Abs(dados)
	if err != nil {
		return c, err
	}
	c.Dados = abs
	if st, err := os.Stat(abs); err != nil || !st.IsDir() {
		return c, fmt.Errorf("pasta de dados não encontrada: %s", abs)
	}
	b, err := os.ReadFile(filepath.Join(abs, "config", "lab.json"))
	if err == nil {
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("config/lab.json inválido: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return c, err
	}
	for _, r := range c.RedesPermitidas {
		_, n, err := net.ParseCIDR(r)
		if err != nil {
			return c, fmt.Errorf("rede inválida em redes_permitidas: %s", r)
		}
		c.redes = append(c.redes, n)
	}
	c.Logs = filepath.Join(abs, "estado", "logs")
	return c, nil
}

// Permitido aceita a própria máquina e redes privadas. Com
// redes_permitidas, aceita só as redes listadas e a própria máquina.
func (c *Config) Permitido(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	if len(c.redes) > 0 {
		for _, n := range c.redes {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	return ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// Plataforma no formato das pastas de runtime, como linux-amd64.
func Plataforma() string { return runtime.GOOS + "-" + runtime.GOARCH }

func (c *Config) Caminho(partes ...string) string {
	return filepath.Join(append([]string{c.Dados}, partes...)...)
}
