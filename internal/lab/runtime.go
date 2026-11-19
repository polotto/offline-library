package lab

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Programas de apoio oficiais, com resumo SHA-256 fixado. Ficam em
// DADOS/runtime/<plataforma>/ e vão junto no disco.
type pacote struct {
	url, sha256 string
	// escolher devolve o caminho de destino (relativo à pasta da
	// plataforma) para um caminho do arquivo compactado, ou "" para pular.
	escolher func(nome string) string
}

func kiwix(arquivo, resumo string) pacote {
	return pacote{"https://download.kiwix.org/release/kiwix-tools/" + arquivo, resumo, func(n string) string {
		base := path.Base(n)
		if strings.HasPrefix(base, "kiwix-serve") || strings.HasSuffix(base, ".dll") {
			return base
		}
		return ""
	}}
}

func jre(arquivo, resumo string, mac bool) pacote {
	return pacote{"https://github.com/adoptium/temurin21-binaries/releases/download/jdk-21.0.13%2B9/" + arquivo, resumo,
		func(n string) string {
			partes := strings.SplitN(n, "/", 2)
			if len(partes) < 2 {
				return ""
			}
			resto := partes[1]
			if mac {
				if !strings.HasPrefix(resto, "Contents/Home/") {
					return ""
				}
				resto = strings.TrimPrefix(resto, "Contents/Home/")
			}
			if resto == "" {
				return ""
			}
			return "jre/" + resto
		}}
}

func pmtilesPkg(arquivo, resumo string) pacote {
	return pacote{"https://github.com/protomaps/go-pmtiles/releases/download/v1.31.2/" + arquivo, resumo, func(n string) string {
		if b := path.Base(n); b == "pmtiles" || b == "pmtiles.exe" {
			return b
		}
		return ""
	}}
}

var pacotesRuntime = map[string][]pacote{
	"linux-amd64": {
		kiwix("kiwix-tools_linux-x86_64-3.8.2.tar.gz", "b0ae98dd344aa0469a15ab42feff6d5aafb79541a82fb4e2647c74b073123815"),
		jre("OpenJDK21U-jre_x64_linux_hotspot_21.0.13_9.tar.gz", "a535a1df9c6284d9618872984e026ae0352d1f7e9e0605425c0e6114f47351dd", false),
		pmtilesPkg("go-pmtiles_1.31.2_Linux_x86_64.tar.gz", "3ed7dbf4ec2e6dfe5e25b6f70d1ffc932729f93c86db353bf514dd71010a312f"),
	},
	"linux-arm64": {
		kiwix("kiwix-tools_linux-aarch64-3.8.2.tar.gz", "a9898fc5363437327350db669bbc03dcc7ecfd498dae727d7184bebe77019514"),
		jre("OpenJDK21U-jre_aarch64_linux_hotspot_21.0.13_9.tar.gz", "b1734db7ade49c3f6acc6ae06233b8c2dd90e3e2f00f67000ae1c972aaa64d8c", false),
		pmtilesPkg("go-pmtiles_1.31.2_Linux_arm64.tar.gz", "f8bd47e7ea866863489cad588fbaf2f31f42e5821f7a03f009b3769f05801cb1"),
	},
	"darwin-arm64": {
		kiwix("kiwix-tools_macos-arm64-3.8.2.tar.gz", "5c64d43176627e558a117146b02ea36b7da2b0cd3a332cbff075cde05d9585f9"),
		jre("OpenJDK21U-jre_aarch64_mac_hotspot_21.0.13_9.tar.gz", "225bab0a09491ef06835488cd942f5d2befa9a0410a2484f0c8bbcf7506ec09a", true),
		pmtilesPkg("go-pmtiles-1.31.2_Darwin_arm64.zip", "40528f7f616fcbf91207cd48c8fc023d213f6d86c0cbf1f748732803d1880f3d"),
	},
	"darwin-amd64": {
		kiwix("kiwix-tools_macos-x86_64-3.8.2.tar.gz", "ae341280e97ad5902e7a4282a24340eac7d7061c267661c0ef2a88a34547f247"),
		jre("OpenJDK21U-jre_x64_mac_hotspot_21.0.13_9.tar.gz", "c909674b1c10c261b9a3fb7e9c0efebd303e33f88a4fe84be7135b5e2d711f89", true),
		pmtilesPkg("go-pmtiles-1.31.2_Darwin_x86_64.zip", "1f0dc02eee6c58312dd6c509faee1b5c32f0596568af1bf51f1b034e7a88a65b"),
	},
	"windows-amd64": {
		kiwix("kiwix-tools_win-x86_64-3.8.1.zip", "fcd01ed2b93e9a68632c7863c83b9f66bf64406a66357be1df7b8b75596f3e45"),
		jre("OpenJDK21U-jre_x64_windows_hotspot_21.0.13_9.zip", "2332c0b4cff6bed722da3567c91d5e47568bcba863fa4945d0a8d6fc76158628", false),
		pmtilesPkg("go-pmtiles_1.31.2_Windows_x86_64.zip", "a658baa4d7e55020aef6ca17bd9ff9faa1582671266b36f58c52db0ac8e785a1"),
	},
}

func PlataformasRuntime() []string {
	return []string{"linux-amd64", "linux-arm64", "darwin-arm64", "darwin-amd64", "windows-amd64"}
}

// PrepararRuntime baixa, confere e extrai os programas de apoio das
// plataformas pedidas. Precisa de internet só aqui.
func PrepararRuntime(ctx context.Context, cfg *Config, plataformas []string) error {
	cache := cfg.CacheRuntime()
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return err
	}
	for _, plat := range plataformas {
		pacotes, ok := pacotesRuntime[plat]
		if !ok {
			return fmt.Errorf("plataforma desconhecida: %s", plat)
		}
		destino := cfg.PastaRuntime(plat)
		novo := destino + ".novo"
		_ = os.RemoveAll(novo)
		for _, p := range pacotes {
			arquivo := filepath.Join(cache, path.Base(strings.ReplaceAll(p.url, "%2B", "+")))
			if err := baixarConferido(ctx, p.url, arquivo, p.sha256); err != nil {
				return fmt.Errorf("%s: %w", plat, err)
			}
			if err := extrair(arquivo, novo, p.escolher); err != nil {
				return fmt.Errorf("%s: extrair %s: %w", plat, path.Base(arquivo), err)
			}
		}
		if err := gravarConferencia(novo); err != nil {
			return fmt.Errorf("%s: conferência: %w", plat, err)
		}
		_ = os.RemoveAll(destino)
		if err := os.Rename(novo, destino); err != nil {
			return err
		}
		if err := ConferirRuntime(cfg, plat); err != nil {
			return fmt.Errorf("%s: gravado com defeito no disco: %w", plat, err)
		}
		log.Printf("runtime: %s pronto e conferido", plat)
	}
	return nil
}

func baixarConferido(ctx context.Context, url, arquivo, resumo string) error {
	if s, err := sha256Arquivo(arquivo); err == nil && s == resumo {
		return nil
	}
	log.Printf("runtime: baixando %s", url)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	tmp := arquivo + ".parcial"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if got := hex.EncodeToString(h.Sum(nil)); got != resumo {
		os.Remove(tmp)
		return fmt.Errorf("resumo SHA-256 diferente do fixado para %s", path.Base(arquivo))
	}
	return os.Rename(tmp, arquivo)
}

func dentro(base, alvo string) bool {
	rel, err := filepath.Rel(base, alvo)
	return err == nil && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

// extrair grava arquivos comuns. Links simbólicos viram cópia do alvo,
// porque discos exFAT não guardam links.
func extrair(arquivo, destino string, escolher func(string) string) error {
	gravar := func(nome string, modo os.FileMode, r io.Reader) error {
		rel := escolher(strings.TrimPrefix(nome, "./"))
		if rel == "" {
			return nil
		}
		alvo := filepath.Join(destino, filepath.FromSlash(rel))
		if !dentro(destino, alvo) {
			return fmt.Errorf("caminho inválido no pacote: %s", nome)
		}
		if err := os.MkdirAll(filepath.Dir(alvo), 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(alvo, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, modo|0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(f, r)
		if e := f.Close(); err == nil {
			err = e
		}
		return err
	}
	if strings.HasSuffix(arquivo, ".zip") {
		z, err := zip.OpenReader(arquivo)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			if f.FileInfo().IsDir() {
				continue
			}
			r, err := f.Open()
			if err != nil {
				return err
			}
			modo := f.Mode().Perm()
			if modo == 0 {
				modo = 0o755
			}
			err = gravar(f.Name, modo, r)
			r.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	abrir := func() (*tar.Reader, func(), error) {
		f, err := os.Open(arquivo)
		if err != nil {
			return nil, nil, err
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return tar.NewReader(gz), func() { gz.Close(); f.Close() }, nil
	}
	tr, fechar, err := abrir()
	if err != nil {
		return err
	}
	links := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			fechar()
			return err
		}
		switch h.Typeflag {
		case tar.TypeReg:
			if err := gravar(h.Name, os.FileMode(h.Mode).Perm(), tr); err != nil {
				fechar()
				return err
			}
		case tar.TypeSymlink:
			links[strings.TrimPrefix(h.Name, "./")] = path.Clean(path.Join(path.Dir(strings.TrimPrefix(h.Name, "./")), h.Linkname))
		}
	}
	fechar()
	if len(links) == 0 {
		return nil
	}
	// Segunda leitura: copia o conteúdo do alvo de cada link.
	alvos := map[string][]string{}
	for link, alvo := range links {
		for i := 0; i < 5; i++ { // link para link
			if prox, ok := links[alvo]; ok {
				alvo = prox
			}
		}
		alvos[alvo] = append(alvos[alvo], link)
	}
	tr, fechar, err = abrir()
	if err != nil {
		return err
	}
	defer fechar()
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		lista := alvos[strings.TrimPrefix(h.Name, "./")]
		if h.Typeflag != tar.TypeReg || len(lista) == 0 {
			continue
		}
		dados, err := io.ReadAll(tr)
		if err != nil {
			return err
		}
		for _, link := range lista {
			if err := gravar(link, os.FileMode(h.Mode).Perm(), strings.NewReader(string(dados))); err != nil {
				return err
			}
		}
	}
	return nil
}

const arquivoConferencia = ".conferencia.json"

// gravarConferencia guarda o SHA-256 de cada arquivo extraído, para
// conferir depois se o disco devolve os mesmos bytes.
func gravarConferencia(pasta string) error {
	resumos := map[string]string{}
	err := filepath.WalkDir(pasta, func(c string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(pasta, c)
		if rel == arquivoConferencia {
			return nil
		}
		s, err := sha256Arquivo(c)
		if err != nil {
			return err
		}
		resumos[filepath.ToSlash(rel)] = s
		return nil
	})
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(resumos, "", " ")
	return os.WriteFile(filepath.Join(pasta, arquivoConferencia), b, 0o644)
}

// ConferirRuntime relê do disco cada arquivo dos programas de apoio da
// plataforma e compara com a conferência gravada na extração.
func ConferirRuntime(cfg *Config, plat string) error {
	pasta := cfg.PastaRuntime(plat)
	b, err := os.ReadFile(filepath.Join(pasta, arquivoConferencia))
	if err != nil {
		return fmt.Errorf("sem conferência gravada")
	}
	var resumos map[string]string
	if err := json.Unmarshal(b, &resumos); err != nil {
		return err
	}
	for rel, esperado := range resumos {
		c := filepath.Join(pasta, filepath.FromSlash(rel))
		esquecerCache(c)
		s, err := sha256Arquivo(c)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if s != esperado {
			return fmt.Errorf("%s com bytes diferentes", rel)
		}
	}
	return nil
}
