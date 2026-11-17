package lab

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/polotto/offline-library/web"
	"golang.org/x/crypto/bcrypt"
)

const esquemaCatalogo = `
CREATE TABLE IF NOT EXISTS itens (
  id INTEGER PRIMARY KEY,
  titulo TEXT NOT NULL,
  autores TEXT NOT NULL DEFAULT '',
  idioma TEXT NOT NULL DEFAULT '',
  assunto TEXT NOT NULL DEFAULT '',
  descricao TEXT NOT NULL DEFAULT '',
  data TEXT NOT NULL DEFAULT '',
  tipo TEXT NOT NULL DEFAULT '',
  publico INTEGER NOT NULL DEFAULT 1,
  origem TEXT NOT NULL DEFAULT '',
  criado TEXT NOT NULL,
  alterado TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tags (
  item_id INTEGER NOT NULL REFERENCES itens(id) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  PRIMARY KEY (item_id, tag)
);
CREATE INDEX IF NOT EXISTS tags_tag ON tags(tag);
CREATE TABLE IF NOT EXISTS arquivos (
  id INTEGER PRIMARY KEY,
  item_id INTEGER NOT NULL REFERENCES itens(id) ON DELETE CASCADE,
  nome TEXT NOT NULL,
  caminho TEXT NOT NULL,
  tamanho INTEGER NOT NULL,
  tipo_mime TEXT NOT NULL DEFAULT '',
  criado TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS usuarios (nome TEXT PRIMARY KEY, hash TEXT NOT NULL);
CREATE VIRTUAL TABLE IF NOT EXISTS itens_fts USING fts5(
  titulo, autores, assunto, descricao, tags,
  tokenize = 'unicode61 remove_diacritics 2');
`

// Catalogo guarda livros e documentos em SQLite, com arquivos no disco.
type Catalogo struct {
	cfg     *Config
	db      *sql.DB
	dir     string
	segredo []byte
	tpl     *template.Template
}

type Item struct {
	ID                                                   int64
	Titulo, Autores, Idioma, Assunto, Descricao, Data    string
	Tipo, Origem                                         string
	Publico                                              bool
	Tags                                                 []string
	Arquivos                                             []Arquivo
}

type Arquivo struct {
	ID, ItemID, Tamanho int64
	Nome, Caminho, Mime string
}

func (a Arquivo) TamanhoLegivel() string { return tamanhoLegivel(a.Tamanho) }

func (i Item) ListaAutores() []string {
	var l []string
	for _, a := range strings.Split(i.Autores, "\n") {
		if a = strings.TrimSpace(a); a != "" {
			l = append(l, a)
		}
	}
	return l
}

func AbrirCatalogo(cfg *Config) (*Catalogo, error) {
	dir := cfg.Caminho("catalogo-lab")
	if err := os.MkdirAll(filepath.Join(dir, "arquivos"), 0o755); err != nil {
		return nil, err
	}
	db, err := abrirSQLite(filepath.Join(dir, "catalogo.db"), false)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(esquemaCatalogo); err != nil {
		db.Close()
		return nil, err
	}
	seg, err := os.ReadFile(filepath.Join(dir, "segredo"))
	if err != nil || len(seg) < 32 {
		seg = make([]byte, 32)
		_, _ = rand.Read(seg)
		if err := os.WriteFile(filepath.Join(dir, "segredo"), seg, 0o600); err != nil {
			return nil, err
		}
	}
	tpl, err := template.New("").Funcs(template.FuncMap{
		"juntar": strings.Join,
		"query":  url.QueryEscape,
	}).ParseFS(web.Arquivos, "catalogo/*.html")
	if err != nil {
		return nil, err
	}
	return &Catalogo{cfg: cfg, db: db, dir: dir, segredo: seg, tpl: tpl}, nil
}

func (c *Catalogo) Fechar() {
	if c.db != nil {
		c.db.Close()
	}
}

func (c *Catalogo) estado() parte {
	var n int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM itens`).Scan(&n); err != nil {
		return parte{"falha", "Banco indisponível: " + err.Error()}
	}
	return parte{"pronto", fmt.Sprintf("%d itens cadastrados.", n)}
}

func (c *Catalogo) Registrar(m *http.ServeMux) {
	m.HandleFunc("GET /catalogo", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/catalogo/", http.StatusMovedPermanently)
	})
	m.HandleFunc("GET /catalogo/{$}", c.lista)
	m.HandleFunc("GET /catalogo/item/{id}", c.item)
	m.HandleFunc("GET /catalogo/arquivo/{id}", c.baixar)
	m.HandleFunc("GET /catalogo/entrar", c.entrarForm)
	m.HandleFunc("POST /catalogo/entrar", c.entrar)
	m.HandleFunc("POST /catalogo/sair", c.sair)
	m.HandleFunc("GET /catalogo/novo", c.exigir(c.formNovo))
	m.HandleFunc("POST /catalogo/novo", c.exigir(c.salvar))
	m.HandleFunc("GET /catalogo/item/{id}/editar", c.exigir(c.formEditar))
	m.HandleFunc("POST /catalogo/item/{id}/editar", c.exigir(c.salvar))
	m.HandleFunc("POST /catalogo/item/{id}/apagar", c.exigir(c.apagar))
	m.HandleFunc("POST /catalogo/arquivo/{id}/apagar", c.exigir(c.apagarArquivo))
}

// --- Sessão

func (c *Catalogo) assinar(v string) string {
	m := hmac.New(sha256.New, c.segredo)
	m.Write([]byte(v))
	return hex.EncodeToString(m.Sum(nil))
}

func (c *Catalogo) usuario(r *http.Request) string {
	ck, err := r.Cookie("lab_sessao")
	if err != nil {
		return ""
	}
	partes := strings.Split(ck.Value, "|")
	if len(partes) != 3 {
		return ""
	}
	if !hmac.Equal([]byte(c.assinar(partes[0]+"|"+partes[1])), []byte(partes[2])) {
		return ""
	}
	exp, _ := strconv.ParseInt(partes[1], 10, 64)
	if time.Now().Unix() > exp {
		return ""
	}
	return partes[0]
}

// mesmaOrigem barra envio de formulário vindo de outro site.
func mesmaOrigem(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

func (c *Catalogo) exigir(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c.usuario(r) == "" {
			http.Redirect(w, r, "/catalogo/entrar?voltar="+url.QueryEscape(r.URL.Path), http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost && !mesmaOrigem(r) {
			http.Error(w, "origem inválida", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (c *Catalogo) temUsuarios() bool {
	var n int
	_ = c.db.QueryRow(`SELECT COUNT(*) FROM usuarios`).Scan(&n)
	return n > 0
}

// DefinirUsuario cria ou troca a senha de um usuário.
func (c *Catalogo) DefinirUsuario(nome, senha string) error {
	if strings.TrimSpace(nome) == "" || len(senha) < 6 {
		return errors.New("nome vazio ou senha com menos de 6 caracteres")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(senha), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = c.db.Exec(`INSERT INTO usuarios(nome, hash) VALUES(?, ?)
		ON CONFLICT(nome) DO UPDATE SET hash = excluded.hash`, strings.TrimSpace(nome), string(h))
	return err
}

func (c *Catalogo) entrarForm(w http.ResponseWriter, r *http.Request) {
	c.render(w, r, "entrar", map[string]any{"Primeiro": !c.temUsuarios(), "Voltar": r.URL.Query().Get("voltar"), "Nome": "", "Erro": ""})
}

func (c *Catalogo) entrar(w http.ResponseWriter, r *http.Request) {
	if !mesmaOrigem(r) {
		http.Error(w, "origem inválida", http.StatusForbidden)
		return
	}
	_ = r.ParseForm()
	nome, senha := strings.TrimSpace(r.FormValue("nome")), r.FormValue("senha")
	dados := map[string]any{"Primeiro": !c.temUsuarios(), "Voltar": r.FormValue("voltar"), "Nome": nome}
	if !c.temUsuarios() {
		if senha != r.FormValue("senha2") {
			dados["Erro"] = "As senhas não conferem."
			c.render(w, r, "entrar", dados)
			return
		}
		if err := c.DefinirUsuario(nome, senha); err != nil {
			dados["Erro"] = "Não foi possível criar: " + err.Error()
			c.render(w, r, "entrar", dados)
			return
		}
		log.Printf("catálogo: administrador %q criado", nome)
	}
	var hash string
	err := c.db.QueryRow(`SELECT hash FROM usuarios WHERE nome = ?`, nome).Scan(&hash)
	if err != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte(senha)) != nil {
		time.Sleep(700 * time.Millisecond)
		dados["Erro"] = "Nome ou senha incorretos."
		c.render(w, r, "entrar", dados)
		return
	}
	exp := strconv.FormatInt(time.Now().Add(30*24*time.Hour).Unix(), 10)
	http.SetCookie(w, &http.Cookie{Name: "lab_sessao", Value: nome + "|" + exp + "|" + c.assinar(nome+"|"+exp),
		Path: "/catalogo/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
	voltar := r.FormValue("voltar")
	if !strings.HasPrefix(voltar, "/catalogo/") || strings.HasPrefix(voltar, "//") {
		voltar = "/catalogo/"
	}
	http.Redirect(w, r, voltar, http.StatusSeeOther)
}

func (c *Catalogo) sair(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "lab_sessao", Value: "", Path: "/catalogo/", MaxAge: -1})
	http.Redirect(w, r, "/catalogo/", http.StatusSeeOther)
}

// --- Consulta

func (c *Catalogo) render(w http.ResponseWriter, r *http.Request, nome string, dados map[string]any) {
	dados["Catalogo"] = c.cfg.NomeCatalogo
	dados["Usuario"] = c.usuario(r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := c.tpl.ExecuteTemplate(w, nome, dados); err != nil {
		log.Printf("catálogo: modelo %s: %v", nome, err)
	}
}

func (c *Catalogo) lista(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	tag := strings.TrimSpace(r.URL.Query().Get("tag"))
	idioma := strings.TrimSpace(r.URL.Query().Get("idioma"))
	logado := c.usuario(r) != ""
	where := []string{"1=1"}
	var args []any
	if !logado {
		where = append(where, "publico = 1")
	}
	if q != "" {
		where = append(where, "id IN (SELECT rowid FROM itens_fts WHERE itens_fts MATCH ?)")
		args = append(args, consultaFTS(q))
	}
	if tag != "" {
		where = append(where, "id IN (SELECT item_id FROM tags WHERE tag = ?)")
		args = append(args, tag)
	}
	if idioma != "" {
		where = append(where, "idioma = ?")
		args = append(args, idioma)
	}
	rows, err := c.db.Query(`SELECT id, titulo, autores, idioma, assunto, data, tipo, publico FROM itens WHERE `+
		strings.Join(where, " AND ")+` ORDER BY titulo COLLATE NOCASE LIMIT 500`, args...)
	if err != nil {
		http.Error(w, "Busca inválida: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer rows.Close()
	var itens []Item
	for rows.Next() {
		var i Item
		_ = rows.Scan(&i.ID, &i.Titulo, &i.Autores, &i.Idioma, &i.Assunto, &i.Data, &i.Tipo, &i.Publico)
		itens = append(itens, i)
	}
	for k := range itens {
		itens[k].Tags = c.tagsDe(itens[k].ID)
	}
	c.render(w, r, "lista", map[string]any{"Itens": itens, "Q": q, "Tag": tag, "Idioma": idioma,
		"Tags": c.todasTags(logado), "Idiomas": c.idiomas(logado)})
}

func (c *Catalogo) tagsDe(id int64) []string {
	rows, err := c.db.Query(`SELECT tag FROM tags WHERE item_id = ? ORDER BY tag`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var l []string
	for rows.Next() {
		var t string
		_ = rows.Scan(&t)
		l = append(l, t)
	}
	return l
}

func (c *Catalogo) todasTags(logado bool) []string {
	filtro := ""
	if !logado {
		filtro = "WHERE item_id IN (SELECT id FROM itens WHERE publico = 1)"
	}
	rows, err := c.db.Query(`SELECT DISTINCT tag FROM tags ` + filtro + ` ORDER BY tag COLLATE NOCASE`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var l []string
	for rows.Next() {
		var t string
		_ = rows.Scan(&t)
		l = append(l, t)
	}
	return l
}

func (c *Catalogo) idiomas(logado bool) []string {
	filtro := "WHERE idioma <> ''"
	if !logado {
		filtro += " AND publico = 1"
	}
	rows, err := c.db.Query(`SELECT DISTINCT idioma FROM itens ` + filtro + ` ORDER BY idioma`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var l []string
	for rows.Next() {
		var t string
		_ = rows.Scan(&t)
		l = append(l, t)
	}
	return l
}

func (c *Catalogo) carregar(id int64) (*Item, error) {
	var i Item
	err := c.db.QueryRow(`SELECT id, titulo, autores, idioma, assunto, descricao, data, tipo, publico, origem
		FROM itens WHERE id = ?`, id).Scan(&i.ID, &i.Titulo, &i.Autores, &i.Idioma, &i.Assunto,
		&i.Descricao, &i.Data, &i.Tipo, &i.Publico, &i.Origem)
	if err != nil {
		return nil, err
	}
	i.Tags = c.tagsDe(id)
	rows, err := c.db.Query(`SELECT id, item_id, nome, caminho, tamanho, tipo_mime FROM arquivos WHERE item_id = ? ORDER BY id`, id)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var a Arquivo
			_ = rows.Scan(&a.ID, &a.ItemID, &a.Nome, &a.Caminho, &a.Tamanho, &a.Mime)
			i.Arquivos = append(i.Arquivos, a)
		}
	}
	return &i, nil
}

func idDe(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func (c *Catalogo) item(w http.ResponseWriter, r *http.Request) {
	i, err := c.carregar(idDe(r))
	if err != nil || (!i.Publico && c.usuario(r) == "") {
		http.NotFound(w, r)
		return
	}
	c.render(w, r, "item", map[string]any{"Item": i})
}

func (c *Catalogo) baixar(w http.ResponseWriter, r *http.Request) {
	var a Arquivo
	var publico bool
	err := c.db.QueryRow(`SELECT a.nome, a.caminho, a.tipo_mime, i.publico FROM arquivos a JOIN itens i ON i.id = a.item_id
		WHERE a.id = ?`, idDe(r)).Scan(&a.Nome, &a.Caminho, &a.Mime, &publico)
	if err != nil || (!publico && c.usuario(r) == "") {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(c.dir, "arquivos", filepath.FromSlash(a.Caminho)))
	if err != nil {
		http.Error(w, "Arquivo ausente no disco.", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	if a.Mime != "" {
		w.Header().Set("Content-Type", a.Mime)
	}
	disp := "attachment"
	if strings.HasPrefix(a.Mime, "image/") || a.Mime == "application/pdf" || strings.HasPrefix(a.Mime, "audio/") ||
		strings.HasPrefix(a.Mime, "video/") || strings.HasPrefix(a.Mime, "text/plain") {
		disp = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": a.Nome}))
	w.Header().Set("Content-Security-Policy", "sandbox")
	http.ServeContent(w, r, a.Nome, st.ModTime(), f)
}

// --- Cadastro

func (c *Catalogo) formNovo(w http.ResponseWriter, r *http.Request) {
	c.render(w, r, "form", map[string]any{"Item": &Item{Publico: true, Idioma: "pt-BR"}, "Novo": true, "TagsTexto": ""})
}

func (c *Catalogo) formEditar(w http.ResponseWriter, r *http.Request) {
	i, err := c.carregar(idDe(r))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	c.render(w, r, "form", map[string]any{"Item": i, "TagsTexto": strings.Join(i.Tags, ", ")})
}

type envio struct{ nome, tmp string; tamanho int64 }

// salvar lê o formulário em fluxo: arquivos vão direto ao disco, sem
// passar pela memória nem pela pasta temporária do sistema.
func (c *Catalogo) salvar(w http.ResponseWriter, r *http.Request) {
	id := idDe(r)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "Formulário inválido.", http.StatusBadRequest)
		return
	}
	campos := map[string]string{}
	var envios []envio
	tmpDir := filepath.Join(c.dir, "tmp")
	_ = os.MkdirAll(tmpDir, 0o755)
	limpar := func() {
		for _, e := range envios {
			os.Remove(e.tmp)
		}
	}
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			limpar()
			http.Error(w, "Envio interrompido: "+err.Error(), http.StatusBadRequest)
			return
		}
		if p.FileName() == "" {
			b, _ := io.ReadAll(io.LimitReader(p, 1<<16))
			campos[p.FormName()] = string(b)
			continue
		}
		nome := filepath.Base(strings.ReplaceAll(p.FileName(), "\\", "/"))
		if extensaoProibida(nome) {
			limpar()
			http.Error(w, "Programas executáveis não são aceitos: "+nome, http.StatusBadRequest)
			return
		}
		f, err := os.CreateTemp(tmpDir, "envio-*")
		if err != nil {
			limpar()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		n, err := io.Copy(f, p)
		f.Close()
		envios = append(envios, envio{nome, f.Name(), n})
		if err != nil {
			limpar()
			http.Error(w, "Envio interrompido: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	titulo := strings.TrimSpace(campos["titulo"])
	if titulo == "" {
		limpar()
		http.Error(w, "O título é obrigatório.", http.StatusBadRequest)
		return
	}
	item := Item{ID: id, Titulo: titulo, Autores: normalizarLinhas(campos["autores"]),
		Idioma: strings.TrimSpace(campos["idioma"]), Assunto: strings.TrimSpace(campos["assunto"]),
		Descricao: strings.TrimSpace(campos["descricao"]), Data: strings.TrimSpace(campos["data"]),
		Tipo: strings.TrimSpace(campos["tipo"]), Publico: campos["publico"] != "",
		Tags: separarTags(campos["tags"])}
	novoID, err := c.Gravar(&item)
	if err != nil {
		limpar()
		http.Error(w, "Não foi possível salvar: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for _, e := range envios {
		if e.tamanho == 0 {
			os.Remove(e.tmp)
			continue
		}
		if err := c.AnexarDe(novoID, e.nome, e.tmp, true); err != nil {
			log.Printf("catálogo: anexo %s: %v", e.nome, err)
		}
	}
	http.Redirect(w, r, fmt.Sprintf("/catalogo/item/%d", novoID), http.StatusSeeOther)
}

func extensaoProibida(nome string) bool {
	switch strings.ToLower(filepath.Ext(nome)) {
	case ".exe", ".bat", ".cmd", ".com", ".msi", ".sh", ".command", ".app", ".scr", ".ps1", ".vbs", ".jar", ".dll", ".so", ".dylib", ".php", ".html", ".htm", ".svg":
		return true
	}
	return false
}

func normalizarLinhas(s string) string {
	var l []string
	for _, a := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		if a = strings.TrimSpace(a); a != "" {
			l = append(l, a)
		}
	}
	return strings.Join(l, "\n")
}

func separarTags(s string) []string {
	vistos := map[string]bool{}
	var l []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		if t != "" && !vistos[strings.ToLower(t)] {
			vistos[strings.ToLower(t)] = true
			l = append(l, t)
		}
	}
	return l
}

// Gravar cria (ID 0) ou atualiza um item, com tags e índice de busca.
func (c *Catalogo) Gravar(i *Item) (int64, error) {
	agora := time.Now().UTC().Format(time.RFC3339)
	tx, err := c.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	id := i.ID
	if id == 0 {
		res, err := tx.Exec(`INSERT INTO itens(titulo, autores, idioma, assunto, descricao, data, tipo, publico, origem, criado, alterado)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, i.Titulo, i.Autores, i.Idioma, i.Assunto, i.Descricao, i.Data, i.Tipo, i.Publico, i.Origem, agora, agora)
		if err != nil {
			return 0, err
		}
		id, _ = res.LastInsertId()
	} else {
		res, err := tx.Exec(`UPDATE itens SET titulo=?, autores=?, idioma=?, assunto=?, descricao=?, data=?, tipo=?, publico=?, alterado=?
			WHERE id=?`, i.Titulo, i.Autores, i.Idioma, i.Assunto, i.Descricao, i.Data, i.Tipo, i.Publico, agora, id)
		if err != nil {
			return 0, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return 0, errors.New("item inexistente")
		}
	}
	if _, err := tx.Exec(`DELETE FROM tags WHERE item_id = ?`, id); err != nil {
		return 0, err
	}
	for _, t := range i.Tags {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO tags(item_id, tag) VALUES(?, ?)`, id, t); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(`DELETE FROM itens_fts WHERE rowid = ?`, id); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO itens_fts(rowid, titulo, autores, assunto, descricao, tags) VALUES(?,?,?,?,?,?)`,
		id, i.Titulo, i.Autores, i.Assunto, i.Descricao, strings.Join(i.Tags, " ")); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// AnexarDe guarda um arquivo no item. mover=true move em vez de copiar.
func (c *Catalogo) AnexarDe(itemID int64, nome, origem string, mover bool) error {
	nome = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, r) || r < 32 {
			return '_'
		}
		return r
	}, nome)
	relDir := strconv.FormatInt(itemID, 10)
	dest := filepath.Join(c.dir, "arquivos", relDir)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	final := nome
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(dest, final)); errors.Is(err, os.ErrNotExist) {
			break
		}
		ext := filepath.Ext(nome)
		final = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(nome, ext), n, ext)
	}
	alvo := filepath.Join(dest, final)
	if mover {
		if err := os.Rename(origem, alvo); err != nil {
			return err
		}
	} else if err := copiarArquivo(origem, alvo); err != nil {
		return err
	}
	st, err := os.Stat(alvo)
	if err != nil {
		return err
	}
	tipo := mime.TypeByExtension(strings.ToLower(filepath.Ext(final)))
	_, err = c.db.Exec(`INSERT INTO arquivos(item_id, nome, caminho, tamanho, tipo_mime, criado) VALUES(?,?,?,?,?,?)`,
		itemID, nome, relDir+"/"+final, st.Size(), tipo, time.Now().UTC().Format(time.RFC3339))
	return err
}

func copiarArquivo(de, para string) error {
	in, err := os.Open(de)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(para + ".parcial")
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(para + ".parcial")
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(para+".parcial", para)
}

func (c *Catalogo) apagar(w http.ResponseWriter, r *http.Request) {
	id := idDe(r)
	if _, err := c.db.Exec(`DELETE FROM itens WHERE id = ?`, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = c.db.Exec(`DELETE FROM itens_fts WHERE rowid = ?`, id)
	_ = os.RemoveAll(filepath.Join(c.dir, "arquivos", strconv.FormatInt(id, 10)))
	http.Redirect(w, r, "/catalogo/", http.StatusSeeOther)
}

func (c *Catalogo) apagarArquivo(w http.ResponseWriter, r *http.Request) {
	var item int64
	var caminho string
	if err := c.db.QueryRow(`SELECT item_id, caminho FROM arquivos WHERE id = ?`, idDe(r)).Scan(&item, &caminho); err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = c.db.Exec(`DELETE FROM arquivos WHERE id = ?`, idDe(r))
	_ = os.Remove(filepath.Join(c.dir, "arquivos", filepath.FromSlash(caminho)))
	http.Redirect(w, r, fmt.Sprintf("/catalogo/item/%d/editar", item), http.StatusSeeOther)
}
