//go:build windows

package modelcatalog

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run() == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
