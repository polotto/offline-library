// Comando lab: servidor da biblioteca offline e ferramentas de manutenção.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/polotto/offline-library/internal/lab"
)

const ajuda = `Uso: lab [comando] [opções]

Comandos:
  servir            sobe a biblioteca (padrão)
  gerar-busca       gera mapas/busca.db a partir de um arquivo .osm.pbf
  preparar-runtime  baixa Kiwix, Java e pmtiles oficiais para cada sistema (precisa de internet)
  importar-omeka    importa itens exportados do Omeka para o catálogo
  usuario           cria ou troca a senha de um administrador do catálogo
  versao            mostra a versão

Opções comuns:
  --dados PASTA     pasta de dados (padrão: pasta acima do executável, ou LAB_DADOS)
`

func pastaDados(flagValor string) string {
	if flagValor != "" {
		return flagValor
	}
	if v := os.Getenv("LAB_DADOS"); v != "" {
		return v
	}
	// No disco, o executável fica em DADOS/bin/lab-plataforma.
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		return filepath.Dir(filepath.Dir(exe))
	}
	return "."
}

func main() {
	comando := "servir"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		comando, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(comando, flag.ExitOnError)
	dados := fs.String("dados", "", "pasta de dados")
	porta := fs.Int("porta", 0, "porta HTTP (substitui config/lab.json)")
	endereco := fs.String("endereco", "", "endereço de escuta (substitui config/lab.json)")
	semAtualizar := fs.Bool("sem-atualizar", false, "não baixar edições novas")
	logs := fs.String("logs", "", "pasta de logs (padrão DADOS/estado/logs)")
	pbf := fs.String("pbf", "", "arquivo .osm.pbf (gerar-busca)")
	saida := fs.String("saida", "", "arquivo de saída (gerar-busca; padrão DADOS/mapas/busca.db)")
	exportacao := fs.String("json", "", "exportação do Omeka (importar-omeka)")
	arquivos := fs.String("arquivos", "", "pasta files/original do Omeka (importar-omeka)")
	nome := fs.String("nome", "", "nome do usuário (usuario)")
	plataformas := fs.String("plataformas", "todas", "lista separada por vírgula (preparar-runtime)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, ajuda); fs.PrintDefaults() }
	_ = fs.Parse(args)

	if comando == "versao" {
		fmt.Println(lab.Versao, lab.Plataforma())
		return
	}
	cfg, err := lab.Carregar(pastaDados(*dados))
	if err != nil {
		log.Fatal(err)
	}
	if *porta > 0 {
		cfg.Porta = *porta
	}
	if *endereco != "" {
		cfg.Endereco = *endereco
	}
	if *semAtualizar {
		cfg.Atualizar = false
	}
	if *logs != "" {
		cfg.Logs = *logs
	}
	ctx, parar := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer parar()

	switch comando {
	case "servir":
		_ = os.MkdirAll(cfg.Logs, 0o755)
		if f, err := os.OpenFile(filepath.Join(cfg.Logs, "lab.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			log.SetOutput(io.MultiWriter(os.Stderr, f))
		}
		s, err := lab.NovoServidor(&cfg)
		if err != nil {
			log.Fatal(err)
		}
		if err := s.Iniciar(ctx); err != nil {
			log.Fatal(err)
		}
	case "gerar-busca":
		if *pbf == "" {
			log.Fatal("informe --pbf arquivo.osm.pbf")
		}
		out := *saida
		if out == "" {
			out = cfg.Caminho("mapas", "busca.db")
		}
		if err := lab.GerarBusca(ctx, *pbf, out); err != nil {
			log.Fatal(err)
		}
	case "preparar-runtime":
		plats := lab.PlataformasRuntime()
		if *plataformas != "todas" {
			plats = strings.Split(*plataformas, ",")
		}
		if err := lab.PrepararRuntime(ctx, &cfg, plats); err != nil {
			log.Fatal(err)
		}
	case "importar-omeka":
		cat, err := lab.AbrirCatalogo(&cfg)
		if err != nil {
			log.Fatal(err)
		}
		defer cat.Fechar()
		n, err := cat.ImportarOmeka(*exportacao, *arquivos)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%d itens importados.\n", n)
	case "usuario":
		if *nome == "" {
			log.Fatal("informe --nome")
		}
		fmt.Fprint(os.Stderr, "Senha (lida da entrada padrão): ")
		senha, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		cat, err := lab.AbrirCatalogo(&cfg)
		if err != nil {
			log.Fatal(err)
		}
		defer cat.Fechar()
		if err := cat.DefinirUsuario(*nome, strings.TrimRight(senha, "\r\n")); err != nil {
			log.Fatal(err)
		}
		fmt.Fprintln(os.Stderr, "\nUsuário gravado.")
	default:
		fs.Usage()
		os.Exit(2)
	}
}
