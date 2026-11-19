package lab

import (
	"os"

	"golang.org/x/sys/unix"
)

// esquecerCache tira o arquivo do cache de memória, para a próxima
// leitura vir do disco. Sem isso, conferir logo depois de gravar lê a
// cópia em memória e não pega gravação corrompida.
func esquecerCache(caminho string) {
	f, err := os.Open(caminho)
	if err != nil {
		return
	}
	defer f.Close()
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}
