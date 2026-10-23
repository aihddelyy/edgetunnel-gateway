//go:build windows

package updater

import (
	"fmt"
	"os/exec"
)

func runRestartProcess(exe string, args []string) error {
	cmdStr := fmt.Sprintf("timeout /t 2 /nobreak >nul & start \"\" \"%s\"", exe)
	for _, a := range args {
		cmdStr += fmt.Sprintf(" \"%s\"", a)
	}
	return exec.Command("cmd.exe", "/c", cmdStr).Start()
}
