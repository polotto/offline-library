//go:build !windows

package lab

import (
	"os/exec"

	"golang.org/x/sys/unix"
)

func esconderJanela(*exec.Cmd) {}

// EspacoDisco devolve bytes livres e totais da pasta.
func EspacoDisco(caminho string) (livre, total uint64, err error) {
	var s unix.Statfs_t
	if err = unix.Statfs(caminho, &s); err != nil {
		return
	}
	return uint64(s.Bavail) * uint64(s.Bsize), uint64(s.Blocks) * uint64(s.Bsize), nil
}
