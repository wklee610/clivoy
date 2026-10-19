package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// createInitialKnowledgeDirectories never changes existing directory contents.
// Its rollback removes only directories created here that are still empty and
// have the same identity, preserving pre-existing or concurrently added data.
func createInitialKnowledgeDirectories(configDir string) (rollback func(), resultErr error) {
	type createdDirectory struct {
		path string
		info os.FileInfo
	}
	var created []createdDirectory
	rollback = func() {
		for i := len(created) - 1; i >= 0; i-- {
			dir := created[i]
			current, err := os.Lstat(dir.path)
			if err == nil && current.IsDir() && os.SameFile(dir.info, current) {
				_ = os.Remove(dir.path)
			}
		}
	}
	defer func() {
		if resultErr != nil {
			rollback()
		}
	}()
	parent := filepath.Join(configDir, "knowledge")
	for _, path := range []string{parent, filepath.Join(parent, "default")} {
		if err := os.Mkdir(path, 0700); err != nil {
			if !os.IsExist(err) {
				return rollback, fmt.Errorf("create knowledge directory: %w", err)
			}
			info, statErr := os.Lstat(path)
			if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return rollback, fmt.Errorf("knowledge directory must be a directory, not a file or symlink")
			}
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			return rollback, fmt.Errorf("open new knowledge directory: %w", err)
		}
		info, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil {
			return rollback, fmt.Errorf("inspect new knowledge directory: %w", statErr)
		}
		created = append(created, createdDirectory{path, info})
		if closeErr != nil {
			return rollback, fmt.Errorf("close new knowledge directory: %w", closeErr)
		}
	}
	return rollback, nil
}
