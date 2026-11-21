package lab

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/polotto/offline-library/web"
)

func (a Arquivo) FormatoLeitor() string {
	switch strings.ToLower(filepath.Ext(a.Nome)) {
	case ".pdf":
		return "PDF"
	case ".epub":
		return "EPUB"
	default:
		return ""
	}
}

func (c *Catalogo) registrarLeitor(m *http.ServeMux) {
	m.Handle("GET /catalogo/leitor/", http.FileServerFS(web.Arquivos))
	m.HandleFunc("GET /catalogo/ler/{id}", c.ler)
	m.HandleFunc("POST /catalogo/ler/{id}", c.exigir(c.salvarApontamento))
}

type apontamento struct {
	ID      int64
	Posicao string
	Texto   string
}

func (c *Catalogo) ler(w http.ResponseWriter, r *http.Request) {
	a, err := c.arquivo(r)
	if err != nil || a.FormatoLeitor() == "" {
		http.NotFound(w, r)
		return
	}
	var notas []apontamento
	if usuario := c.usuario(r); usuario != "" {
		notas, err = c.apontamentos(a.ID, usuario)
		if err != nil {
			http.Error(w, "Não foi possível carregar os apontamentos.", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; "+
		"style-src 'self' 'unsafe-inline' blob:; connect-src 'self' blob:; worker-src 'self' blob:; "+
		"frame-src 'self' blob:; img-src 'self' blob: data:; font-src 'self' blob: data:; "+
		"base-uri 'self'; form-action 'self'; frame-ancestors 'self'")
	c.render(w, r, "leitor", map[string]any{"Arquivo": a, "Apontamentos": notas})
}

func (c *Catalogo) apontamentos(id int64, usuario string) ([]apontamento, error) {
	rows, err := c.db.Query(`SELECT id, posicao, texto FROM apontamentos
		WHERE arquivo_id = ? AND usuario = ? ORDER BY id DESC`, id, usuario)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var notas []apontamento
	for rows.Next() {
		var nota apontamento
		if err := rows.Scan(&nota.ID, &nota.Posicao, &nota.Texto); err != nil {
			return nil, err
		}
		notas = append(notas, nota)
	}
	return notas, rows.Err()
}

var paginaPDF = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)

func (c *Catalogo) salvarApontamento(w http.ResponseWriter, r *http.Request) {
	a, err := c.arquivo(r)
	if err != nil || a.FormatoLeitor() == "" {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Apontamento inválido.", http.StatusBadRequest)
		return
	}
	usuario := c.usuario(r)
	texto := strings.TrimSpace(r.PostForm.Get("texto"))
	acao := r.PostForm.Get("acao")
	nota := r.PostForm.Get("nota")
	posicao := r.PostForm.Get("posicao")
	if acao != "apagar" && (texto == "" || utf8.RuneCountInString(texto) > 4000) {
		http.Error(w, "Escreva um apontamento de até 4.000 caracteres.", http.StatusBadRequest)
		return
	}
	switch acao {
	case "criar":
		if !posicaoValida(a.FormatoLeitor(), posicao) {
			http.Error(w, "Posição de leitura inválida.", http.StatusBadRequest)
			return
		}
		_, err = c.db.Exec(`INSERT INTO apontamentos(arquivo_id, usuario, posicao, texto) VALUES(?,?,?,?)`,
			a.ID, usuario, posicao, texto)
	case "editar", "apagar":
		query := `DELETE FROM apontamentos WHERE id=? AND arquivo_id=? AND usuario=?`
		args := []any{nota, a.ID, usuario}
		if acao == "editar" {
			query = `UPDATE apontamentos SET texto=? WHERE id=? AND arquivo_id=? AND usuario=?`
			args = append([]any{texto}, args...)
		}
		res, execErr := c.db.Exec(query, args...)
		err = execErr
		if err == nil {
			n, _ := res.RowsAffected()
			if n == 0 {
				http.NotFound(w, r)
				return
			}
		}
	default:
		http.Error(w, "Ação inválida.", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "Não foi possível salvar o apontamento.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/catalogo/ler/%d#apontamentos", a.ID), http.StatusSeeOther)
}

func posicaoValida(formato, posicao string) bool {
	if formato == "PDF" {
		return paginaPDF.MatchString(posicao)
	}
	return len(posicao) <= 2048 && strings.HasPrefix(posicao, "epubcfi(") && strings.HasSuffix(posicao, ")")
}
