//go:build !darwin && !linux && !windows

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

func checkPlatform() error {
	return fmt.Errorf("background servers are not supported on %s", runtime.GOOS)
}

func configureDetached(*exec.Cmd) error             { return checkPlatform() }
func openPrivateFile(string, int) (*os.File, error) { return nil, checkPlatform() }
func checkPrivateMode(os.FileInfo) error            { return checkPlatform() }
func lockFile(*os.File) error                       { return checkPlatform() }
func unlockFile(*os.File) error                     { return checkPlatform() }
