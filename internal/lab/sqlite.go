package lab

import (
	"database/sql"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
	"golang.org/x/text/unicode/norm"
)

// abrirSQLite abre o banco com FTS5. Diário em modo delete: o WAL usa
// memória compartilhada, que falha em exFAT e em disco de rede.
func abrirSQLite(caminho string, somenteLeitura bool) (*sql.DB, error) {
	abs, err := filepath.Abs(caminho)
	if err != nil {
		return nil, err
	}
	p := filepath.ToSlash(abs)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/... no Windows
	}
	u := url.URL{Scheme: "file", Path: p}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(15000)")
	q.Add("_pragma", "foreign_keys(1)")
	if somenteLeitura {
		q.Set("mode", "ro")
	} else {
		q.Add("_pragma", "journal_mode(delete)")
	}
	u.RawQuery = q.Encode()
	return driver.Open(u.String(), fts5.Register)
}

func copiar(w io.Writer, r io.Reader) (int64, error) { return io.Copy(w, r) }

var reEspaco = regexp.MustCompile(`\s+`)

// semAcento deixa minúsculo e sem diacríticos, para chaves e consultas.
func semAcento(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(reEspaco.ReplaceAllString(b.String(), " "))
}

// consultaFTS monta uma busca por prefixo: cada palavra precisa aparecer.
func consultaFTS(texto string) string {
	var termos []string
	for _, p := range strings.FieldsFunc(semAcento(texto), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	}) {
		termos = append(termos, `"`+p+`"*`)
		if len(termos) == 8 {
			break
		}
	}
	return strings.Join(termos, " ")
}
