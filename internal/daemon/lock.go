package daemon

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// ErrLocked means another handle or process already holds this lock.
var ErrLocked = errors.New("daemon lock is already held")

// Lock owns an operating-system lock. Keep it open for the entire protected
// operation. The persistent lock file must never be removed, even after Close:
// removing it can allow two processes to lock different files at the same path.
type Lock struct {
	mu   sync.Mutex
	file *os.File
}

// TryLock obtains an exclusive, nonblocking lock, creating a private regular
// file if necessary. Existing files are never truncated or chmodded. Symlinks,
// special files, and Unix files accessible by other users are rejected.
// The parent directory must already exist and be trusted by the caller.
func TryLock(path string) (*Lock, error) {
	if err := checkPlatform(); err != nil {
		return nil, err
	}
	f, err := openLockFile(path)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock %q: %w", path, err)
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock %q: %w", path, err)
	}
	// Check again after locking so replacement during lock acquisition cannot
	// silently leave the caller protecting a different inode.
	if err := validateOpenFile(path, f, nil); err != nil {
		unlockFile(f)
		f.Close()
		return nil, fmt.Errorf("validate daemon lock %q: %w", path, err)
	}
	return &Lock{file: f}, nil
}

// Close unlocks and closes the handle, without removing its file. It is safe to
// call concurrently or repeatedly. Process exit also releases the OS lock.
func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	return errors.Join(unlockFile(f), f.Close())
}

func openLockFile(path string) (*os.File, error) {
	// A competing creator can win between Lstat and O_EXCL. Retry validation
	// instead of opening that new file without checking its type and mode.
	for attempt := 0; attempt < 4; attempt++ {
		before, err := os.Lstat(path)
		flags := os.O_RDWR
		if errors.Is(err, os.ErrNotExist) {
			flags |= os.O_CREATE | os.O_EXCL
		} else if err != nil {
			return nil, err
		} else if err := validateFileInfo(before); err != nil {
			return nil, err
		}
		f, err := openPrivateFile(path, flags)
		if errors.Is(err, os.ErrExist) && flags&os.O_EXCL != 0 {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := validateOpenFile(path, f, before); err != nil {
			f.Close()
			return nil, err
		}
		return f, nil
	}
	return nil, errors.New("lock file changed repeatedly while opening")
}

func validateFileInfo(info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return errors.New("lock path must be a regular file, not a symlink or special file")
	}
	return checkPrivateMode(info)
}

func validateOpenFile(path string, f *os.File, before os.FileInfo) error {
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if err := validateFileInfo(opened); err != nil {
		return err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err := validateFileInfo(after); err != nil {
		return err
	}
	if !os.SameFile(opened, after) || (before != nil && !os.SameFile(before, opened)) {
		return errors.New("lock file changed while opening")
	}
	return nil
}
