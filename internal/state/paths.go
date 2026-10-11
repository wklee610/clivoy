// Package state locates Clivoy's user-owned configuration and logs. Resolving
// paths is read-only; callers explicitly choose when to create the managed
// directories.
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

type Paths struct {
	Root   string
	Config string
	Auth   string
	Logs   string
	Log    string
}

// Resolve selects CLIVOY_HOME when set, otherwise the operating system's user
// home directory followed by .clivoy. All returned paths are absolute.
func Resolve() (Paths, error) {
	root, overridden := os.LookupEnv("CLIVOY_HOME")
	if overridden {
		if !filepath.IsAbs(root) {
			return Paths{}, fmt.Errorf("CLIVOY_HOME must be an absolute path")
		}
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve user home directory: %w", err)
		}
		if !filepath.IsAbs(home) {
			return Paths{}, fmt.Errorf("user home directory must be an absolute path; set CLIVOY_HOME")
		}
		root = filepath.Join(home, ".clivoy")
	}
	root = filepath.Clean(root)
	config := filepath.Join(root, "clivoy.json")
	logs := filepath.Join(root, "logs")
	return Paths{
		Root: root, Config: config, Auth: filepath.Join(root, "auth.json"),
		Logs: logs, Log: filepath.Join(logs, "clivoy.log"),
	}, nil
}

// Ensure creates the managed directories without creating or changing config,
// key, or log files. Existing directories with broad Unix permissions are
// rejected rather than silently changing a user's permissions.
func Ensure(paths Paths) error {
	if !filepath.IsAbs(paths.Root) {
		return fmt.Errorf("Clivoy state root must be an absolute path")
	}
	root := filepath.Clean(paths.Root)
	if filepath.Clean(paths.Logs) != filepath.Join(root, "logs") {
		return fmt.Errorf("Clivoy log directory must be inside the state root")
	}
	for _, path := range []string{root, paths.Logs} {
		if err := ensureDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func ensureDirectory(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0700); err != nil {
			return fmt.Errorf("create Clivoy directory %q: %w", path, err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return fmt.Errorf("inspect Clivoy directory %q: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Clivoy path %q must be a directory, not a symlink or file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("Clivoy directory %q must be owner-only; run chmod 700 %q", path, path)
	}
	return nil
}
