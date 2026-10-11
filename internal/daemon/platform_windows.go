//go:build windows

package daemon

import (
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	lockFileEx   = kernel32.NewProc("LockFileEx")
	unlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func checkPlatform() error { return nil }

func configureDetached(cmd *exec.Cmd) error {
	const detachedProcess = 0x00000008
	const createNewProcessGroup = 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
		HideWindow:    true,
	}
	return nil
}

func openPrivateFile(path string, flags int) (*os.File, error) {
	// Inspect a reparse point itself rather than following a symlink swapped
	// in between Lstat and open. Go's OpenFile uses non-inheritable handles.
	return os.OpenFile(path, flags|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0o600)
}

// Windows access is governed by the enclosing user's directory ACL, not the
// Unix permission bits returned by os.Stat.
func checkPrivateMode(os.FileInfo) error { return nil }

func lockFile(f *os.File) error {
	const lockfileFailImmediately = 0x00000001
	const lockfileExclusiveLock = 0x00000002
	var overlapped syscall.Overlapped
	// Lock one byte at offset zero, also valid for an empty persistent file.
	r, _, err := lockFileEx.Call(f.Fd(), lockfileFailImmediately|lockfileExclusiveLock, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(&overlapped)
	runtime.KeepAlive(f)
	if r != 0 {
		return nil
	}
	if err == syscall.Errno(33) { // ERROR_LOCK_VIOLATION
		return ErrLocked
	}
	return err
}

func unlockFile(f *os.File) error {
	var overlapped syscall.Overlapped
	r, _, err := unlockFileEx.Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	runtime.KeepAlive(&overlapped)
	runtime.KeepAlive(f)
	if r != 0 {
		return nil
	}
	return err
}
