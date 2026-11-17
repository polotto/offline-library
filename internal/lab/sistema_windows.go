//go:build windows

package lab

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// Sem janela de console extra para Kiwix e Java.
func esconderJanela(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

func EspacoDisco(caminho string) (livre, total uint64, err error) {
	p, err := windows.UTF16PtrFromString(caminho)
	if err != nil {
		return
	}
	var livreUsuario uint64
	err = windows.GetDiskFreeSpaceEx(p, &livreUsuario, &total, &livre)
	return livreUsuario, total, err
}
