// Package web guarda as páginas e recursos embutidos no executável.
package web

import "embed"

//go:embed portal mapas catalogo
var Arquivos embed.FS
