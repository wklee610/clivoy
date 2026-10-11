//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package provider

import "os/exec"

// CommandContext's default cancellation terminates the direct process.
func configureProcess(cmd *exec.Cmd) {}
