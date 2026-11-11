package tools

import (
	"context"
	"os/exec"
	"time"
)

// Command is exec.CommandContext for external engines. Cancelling ctx (a
// timeout, the user pressing Cancel) stops the program and everything it
// started: several engines are wrappers that launch the real worker as a
// child (LibreOffice's soffice starts soffice.bin), and killing only the
// wrapper would leave the worker running and holding the output pipes.
// WaitDelay bounds how long Wait then waits for those pipes to close.
func Command(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	killTree(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}
