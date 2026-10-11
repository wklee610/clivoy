//go:build darwin || linux

package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func checkPlatform() error { return nil }

func configureDetached(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return nil
}

func openPrivateFile(path string, flags int) (*os.File, error) {
	// O_NOFOLLOW closes the final-component symlink race. O_NONBLOCK prevents
	// a concurrently substituted FIFO from blocking before f.Stat rejects it.
	return os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0o600)
}

func checkPrivateMode(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("lock file permissions must be private; run chmod 600 on %q", info.Name())
	}
	return nil
}

func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return ErrLocked
		}
		return err
	}
}

func unlockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}
