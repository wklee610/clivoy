//go:build windows

package provider

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
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// taskkill is invoked directly, never through cmd.exe. If unavailable,
		// fall back to terminating the direct child.
		if exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run() == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
}
