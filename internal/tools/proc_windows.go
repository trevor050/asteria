//go:build windows

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// killTree stops the process and its children with taskkill /T on cancel,
// and keeps console engines from flashing a window.
func killTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		tk := filepath.Join(os.Getenv("SystemRoot"), "System32", "taskkill.exe")
		kill := exec.Command(tk, "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
		if err := kill.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
