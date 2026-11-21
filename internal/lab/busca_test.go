package lab

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestBuscaCancelaEsperaPorConexao(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "busca.db")
	indice, err := abrirSQLite(caminho, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = indice.Exec("CREATE TABLE lugares(id INTEGER PRIMARY KEY)")
	if fechar := indice.Close(); err != nil || fechar != nil {
		t.Fatal(errors.Join(err, fechar))
	}

	busca := &Busca{caminho: caminho}
	defer busca.Fechar()
	banco := busca.abrir()
	if banco == nil {
		t.Fatal("índice não abriu")
	}
	banco.SetMaxOpenConns(1)
	conexao, err := banco.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conexao.Close()

	ctx, cancelar := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelar()
	if _, err := busca.Procurar(ctx, "Curitiba", 0, 0, false, 12); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("esperava prazo vencido, recebeu %v", err)
	}
}
