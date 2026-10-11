// Package daemon provides the operating-system primitives used by the CLI's
// background server. Lifecycle and authenticated shutdown live in the CLI.
package daemon

import (
	"fmt"
	"os"
	"os/exec"
)

// Launch starts a detached child and releases the parent process handle. The
// child receives no terminal or pipes; it must arrange its own logging. A nil
// env inherits the current environment, as with os/exec.Cmd.
func Launch(executable string, args []string, env []string, dir string) (int, error) {
	cmd := exec.Command(executable, args...)
	cmd.Env = env
	cmd.Dir = dir
	if err := configureDetached(cmd); err != nil {
		return 0, err
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return 0, fmt.Errorf("open detached process stdio: %w", err)
	}
	defer null.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = null, null, null
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start detached process: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		// The child has already started; preserve its PID for diagnostics.
		return pid, fmt.Errorf("release detached process handle: %w", err)
	}
	return pid, nil
}
