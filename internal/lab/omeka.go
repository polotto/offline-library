package lab

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// ItemOmeka é o formato gerado por scripts/exportar-omeka.sh.
type ItemOmeka struct {
	ID       int64               `json:"id"`
	Publico  int                 `json:"public"`
	Campos   map[string][]string `json:"campos"`
	Tags     []string            `json:"tags"`
	Arquivos []struct {
		Armazenado string `json:"filename"`
		Original   string `json:"original"`
	} `json:"arquivos"`
}

// ImportarOmeka copia itens e arquivos de uma exportação do Omeka.
// Itens já importados (mesma origem) são ignorados.
func (c *Catalogo) ImportarOmeka(arquivoJSON, pastaArquivos string) (int, error) {
	b, err := os.ReadFile(arquivoJSON)
	if err != nil {
		return 0, err
	}
	var itens []ItemOmeka
	if err := json.Unmarshal(b, &itens); err != nil {
		return 0, fmt.Errorf("exportação inválida: %w", err)
	}
	primeiro := func(m map[string][]string, k string) string {
		if v := m[k]; len(v) > 0 {
			return strings.TrimSpace(v[0])
		}
		return ""
	}
	novos := 0
	for _, o := range itens {
		origem := fmt.Sprintf("omeka:%d", o.ID)
		var existe int
		_ = c.db.QueryRow(`SELECT COUNT(*) FROM itens WHERE origem = ?`, origem).Scan(&existe)
		if existe > 0 {
			continue
		}
		titulo := primeiro(o.Campos, "Title")
		if titulo == "" {
			titulo = fmt.Sprintf("Item %d sem título", o.ID)
		}
		item := Item{Titulo: titulo, Autores: strings.Join(o.Campos["Creator"], "\n"),
			Idioma: primeiro(o.Campos, "Language"), Assunto: strings.Join(o.Campos["Subject"], "; "),
			Descricao: strings.Join(o.Campos["Description"], "\n\n"), Data: primeiro(o.Campos, "Date"),
			Tipo: primeiro(o.Campos, "Type"), Publico: o.Publico == 1, Tags: o.Tags, Origem: origem}
		id, err := c.Gravar(&item)
		if err != nil {
			return novos, fmt.Errorf("item %d: %w", o.ID, err)
		}
		for _, a := range o.Arquivos {
			origemArq := filepath.Join(pastaArquivos, a.Armazenado)
			nome := a.Original
			if nome == "" {
				nome = a.Armazenado
			}
			if err := c.AnexarDe(id, filepath.Base(nome), origemArq, false); err != nil {
				log.Printf("importação: item %d, arquivo %s: %v", o.ID, a.Armazenado, err)
			}
		}
		novos++
	}
	return novos, nil
}
