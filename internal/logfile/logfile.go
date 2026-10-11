// Package logfile provides a bounded append-only log sink for one process.
//
// The caller must create the parent directory and protect its ancestors from
// untrusted modification. Open does not create directories. The current log and
// its .1, .2, and .3 backup names are reserved for this writer; .1 is newest.
// Symlinks, nonregular files, and (on Unix) group/other permissions are rejected.
// Newly created files use mode 0600. Windows permissions follow the directory's
// inherited ACLs and must be controlled by the caller.
//
// A Writer is safe for concurrent goroutines. Independent processes or writers
// must not share the same path: this package does not implement file locking or
// coordinate with external log rotation. It stores bytes without interpreting
// or redacting them; callers must avoid logging credentials or request payloads.
package logfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
)

const (
	maxBytes = 10 << 20
	backups  = 3
)

// Writer appends to a private file, rotating during writes at 10 MiB and keeping
// three backups. Large writes are split to keep each new file within the limit.
// A preexisting oversized file is preserved as a backup on the next write.
// Rotation failure stops further writes instead of silently dropping log data.
type Writer struct {
	mu       sync.Mutex
	path     string
	file     *os.File
	size     int64
	limit    int64
	keep     int
	closed   bool
	writeErr error
}

var _ io.WriteCloser = (*Writer)(nil)

// Open opens path for append, creating it with mode 0600 when absent. Its parent
// directory must already exist and must not itself be a symlink. Existing file
// permissions are checked, never silently changed. Open does not truncate logs.
func Open(path string) (*Writer, error) {
	return open(path, maxBytes, backups)
}

func open(path string, limit int64, keep int) (*Writer, error) {
	if path == "" || limit < 1 || keep < 1 {
		return nil, fmt.Errorf("logfile: invalid path or rotation limits")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("logfile: resolve path: %w", err)
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("logfile: inspect parent directory: %w", err)
	}
	if !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("logfile: parent must be a directory, not a symlink")
	}
	w := &Writer{path: path, limit: limit, keep: keep}
	if err := w.checkBackups(); err != nil {
		return nil, err
	}
	file, info, err := openAppend(path)
	if err != nil {
		return nil, err
	}
	w.file, w.size = file, info.Size()
	return w, nil
}

func privateRegular(path string, info os.FileInfo) error {
	if !info.Mode().IsRegular() {
		return fmt.Errorf("logfile: %s must be a regular file, not a symlink or special file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("logfile: %s must not grant group or other permissions", path)
	}
	return nil
}

func inspect(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if err := privateRegular(path, info); err != nil {
		return nil, err
	}
	return info, nil
}

func openAppend(path string) (*os.File, os.FileInfo, error) {
	before, err := inspect(path)
	flags := os.O_WRONLY | os.O_APPEND
	if errors.Is(err, os.ErrNotExist) {
		// Exclusive creation cannot follow a symlink that appears after Lstat.
		flags |= os.O_CREATE | os.O_EXCL
	} else if err != nil {
		return nil, nil, fmt.Errorf("logfile: inspect current log: %w", err)
	}
	file, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return nil, nil, fmt.Errorf("logfile: open current log: %w", err)
	}
	info, err := file.Stat()
	if err == nil {
		err = privateRegular(path, info)
	}
	if err == nil && before != nil && !os.SameFile(before, info) {
		err = fmt.Errorf("logfile: current log changed while opening")
	}
	if err == nil {
		var current os.FileInfo
		current, err = inspect(path)
		if err == nil && !os.SameFile(current, info) {
			err = fmt.Errorf("logfile: current log changed while opening")
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	return file, info, nil
}

func (w *Writer) backup(index int) string {
	return w.path + "." + strconv.Itoa(index)
}

func (w *Writer) checkBackups() error {
	for i := 1; i <= w.keep; i++ {
		if _, err := inspect(w.backup(i)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("logfile: inspect backup: %w", err)
		}
	}
	return nil
}

// Write implements io.Writer. Ordinary writes of at most 10 MiB stay together
// in one file. Larger writes may span backups, with oldest bytes discarded by
// the retention limit. Only successfully appended bytes count toward n.
func (w *Writer) Write(data []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	for len(data) > 0 {
		remaining := w.limit - w.size
		if remaining <= 0 || (w.size > 0 && int64(len(data)) <= w.limit && int64(len(data)) > remaining) {
			if err := w.rotate(); err != nil {
				w.writeErr = fmt.Errorf("logfile: rotate: %w", err)
				return n, w.writeErr
			}
			remaining = w.limit
		}
		chunk := int64(len(data))
		if chunk > remaining {
			chunk = remaining
		}
		written, err := w.file.Write(data[:int(chunk)])
		w.size += int64(written)
		n += written
		data = data[written:]
		if err != nil {
			return n, err
		}
		if int64(written) != chunk {
			return n, io.ErrShortWrite
		}
	}
	return n, nil
}

func (w *Writer) rotate() error {
	current, err := inspect(w.path)
	if err != nil {
		return err
	}
	opened, err := w.file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(current, opened) {
		return fmt.Errorf("current log was replaced outside this writer")
	}
	if err := w.checkBackups(); err != nil {
		return err
	}
	// Close before rename so rotation also works on Windows.
	err = w.file.Close()
	w.file = nil
	if err != nil {
		return err
	}
	if err := os.Remove(w.backup(w.keep)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for i := w.keep - 1; i >= 1; i-- {
		if err := os.Rename(w.backup(i), w.backup(i+1)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(w.path, w.backup(1)); err != nil {
		return err
	}
	// A new file must be created; never append to a concurrently inserted path.
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	w.file, w.size = file, 0
	return nil
}

// Close releases the file and is safe to call more than once.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.file != nil {
		return w.file.Close()
	}
	return nil
}
