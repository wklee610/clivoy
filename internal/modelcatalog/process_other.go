//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package modelcatalog

import "os/exec"

func configureProcess(*exec.Cmd) {}
