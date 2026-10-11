// Package clidiscovery finds installed coding CLIs without running shell
// profiles, package managers, installers, or login commands.
package clidiscovery

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Resolution describes a direct process launch. Args precedes the CLI's own
// arguments (for example, the npm entrypoint when Path is a Node executable).
// Command is the absolute CLI path suitable for displaying in diagnostics.
type Resolution struct {
	Path    string
	Args    []string
	Command string
	nodeDir string
	goos    string
}

// Resolve treats a nonempty configuredCommand as authoritative: only that
// path, or that name in absolute PATH entries, is considered. An empty command
// searches PATH followed by known user/system installation locations.
func Resolve(kind, configuredCommand string) (Resolution, error) {
	if kind != "codex" && kind != "claude" && kind != "gemini" && kind != "grok" {
		return Resolution{}, fmt.Errorf("unsupported CLI kind %q", kind)
	}
	home, _ := os.UserHomeDir()
	dirs, apps := installationLocations(runtime.GOOS, home, os.Getenv)
	r := resolver{goos: runtime.GOOS, path: os.Getenv("PATH"), dirs: dirs, apps: apps}
	return r.resolve(kind, configuredCommand)
}

// Environment preserves the caller's environment (including prior filtering)
// and adds the selected Node directory to PATH when an npm entrypoint needs it.
// Relative and empty PATH entries are omitted to avoid executing programs from
// a request working directory. The supplied slice is never modified.
func (r Resolution) Environment(base []string) []string {
	goos := r.goos
	if goos == "" {
		goos = runtime.GOOS
	}
	var inherited string
	out := make([]string, 0, len(base)+1)
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok && (key == "PATH" || goos == "windows" && strings.EqualFold(key, "PATH")) {
			inherited = value
			continue
		}
		out = append(out, entry)
	}
	dirs := absolutePathEntries(inherited, goos)
	if r.nodeDir != "" {
		dirs = append([]string{r.nodeDir}, dirs...)
	}
	return append(out, "PATH="+strings.Join(uniquePaths(dirs, goos), pathSeparator(goos)))
}

type resolver struct {
	goos string
	path string
	dirs []string
	apps []string // Full codex executable paths, injectable in tests.
}

func (r resolver) resolve(kind, configured string) (Resolution, error) {
	if kind != "codex" && kind != "claude" && kind != "gemini" && kind != "grok" {
		return Resolution{}, fmt.Errorf("unsupported CLI kind %q", kind)
	}
	var candidates []string
	if configured != "" {
		if filepath.IsAbs(configured) || strings.ContainsAny(configured, `/\`) {
			absolute, err := filepath.Abs(configured)
			if err != nil {
				return Resolution{}, fmt.Errorf("resolve configured %s path: %w", kind, err)
			}
			candidates = executableNames(absolute, r.goos)
		} else {
			candidates = pathsIn(absolutePathEntries(r.path, r.goos), configured, r.goos)
		}
	} else {
		dirs := append(absolutePathEntries(r.path, r.goos), r.dirs...)
		candidates = pathsIn(uniquePaths(dirs, r.goos), kind, r.goos)
		if kind == "codex" {
			candidates = append(candidates, r.apps...)
		}
	}
	var detail error
	for _, candidate := range uniquePaths(candidates, r.goos) {
		result, err := r.inspect(kind, candidate)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, os.ErrNotExist) && detail == nil {
			detail = err
		}
	}
	if configured != "" {
		if detail != nil {
			return Resolution{}, fmt.Errorf("configured %s command %q cannot be launched: %w", kind, configured, detail)
		}
		return Resolution{}, fmt.Errorf("configured %s command %q was not found; automatic fallback is disabled", kind, configured)
	}
	if detail != nil {
		return Resolution{}, fmt.Errorf("%s CLI was found but cannot be launched: %w", kind, detail)
	}
	return Resolution{}, fmt.Errorf("%s CLI was not found in PATH or known installation directories; install it or set command to its executable path", kind)
}

func (r resolver) inspect(kind, candidate string) (Resolution, error) {
	info, err := os.Stat(candidate) // Native installers commonly create symlinks.
	if err != nil {
		return Resolution{}, err
	}
	if !info.Mode().IsRegular() {
		return Resolution{}, fmt.Errorf("%s is not a regular executable", candidate)
	}
	ext := strings.ToLower(filepath.Ext(candidate))
	if r.goos == "windows" && (ext == ".cmd" || ext == ".bat") {
		return r.npmWrapper(kind, candidate)
	}
	if r.goos == "windows" && ext == ".ps1" {
		return Resolution{}, errors.New("PowerShell wrappers are not supported; configure the native executable or npm .cmd entrypoint")
	}
	if ext == ".js" || ext == ".cjs" || ext == ".mjs" {
		return r.nodeScript(candidate, candidate)
	}
	if r.goos != "windows" && info.Mode().Perm()&0o111 == 0 {
		return Resolution{}, fmt.Errorf("%s is not executable", candidate)
	}
	if r.goos == "windows" && ext != ".exe" && ext != ".com" {
		return Resolution{}, errors.New("Windows CLI command must be a native executable or supported npm .cmd entrypoint")
	}
	if r.goos != "windows" {
		line, err := firstLine(candidate)
		if err != nil {
			return Resolution{}, err
		}
		words := strings.Fields(strings.TrimPrefix(line, "#!"))
		if strings.HasPrefix(line, "#!") && len(words) >= 2 && words[0] == "/usr/bin/env" {
			if words[1] == "-S" {
				words = append(words[:1], words[2:]...)
			}
			if len(words) >= 2 && words[1] == "node" {
				if len(words) != 2 {
					return Resolution{}, errors.New("Node shebang flags are unsupported; configure a native executable")
				}
				return r.nodeScript(candidate, candidate)
			}
		}
	}
	return Resolution{Path: candidate, Command: candidate, goos: r.goos}, nil
}

func (r resolver) nodeScript(command, script string) (Resolution, error) {
	info, err := os.Stat(script)
	if err != nil || !info.Mode().IsRegular() {
		return Resolution{}, fmt.Errorf("npm CLI entrypoint is missing or not a regular file: %s", script)
	}
	dirs := []string{filepath.Dir(command), filepath.Dir(script)}
	if real, err := filepath.EvalSymlinks(script); err == nil {
		// A global npm script commonly lives under PREFIX/lib/node_modules;
		// its paired interpreter is PREFIX/bin/node. No recursive scan.
		parent := filepath.Dir(real)
		for range 8 {
			if filepath.Base(parent) == "node_modules" {
				prefix := filepath.Dir(parent)
				if filepath.Base(prefix) == "lib" {
					prefix = filepath.Dir(prefix)
				}
				dirs = append(dirs, prefix, filepath.Join(prefix, "bin"))
				break
			}
			next := filepath.Dir(parent)
			if next == parent {
				break
			}
			parent = next
		}
	}
	dirs = append(dirs, absolutePathEntries(r.path, r.goos)...)
	dirs = append(dirs, r.dirs...)
	name := "node"
	if r.goos == "windows" {
		name += ".exe"
	}
	for _, dir := range uniquePaths(dirs, r.goos) {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() || r.goos != "windows" && info.Mode().Perm()&0o111 == 0 {
			continue
		}
		return Resolution{Path: candidate, Args: []string{script}, Command: command, nodeDir: dir, goos: r.goos}, nil
	}
	return Resolution{}, fmt.Errorf("%s needs Node.js, but no paired or installed node executable was found", command)
}

func firstLine(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data := make([]byte, 4096)
	n, err := f.Read(data)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line, _, _ := strings.Cut(string(data[:n]), "\n")
	return strings.TrimSpace(line), nil
}

func pathSeparator(goos string) string {
	if goos == "windows" {
		return ";"
	}
	return ":"
}

func absolutePathEntries(path, goos string) []string {
	var dirs []string
	for _, entry := range strings.Split(path, pathSeparator(goos)) {
		if goos == "windows" {
			entry = strings.Trim(entry, `"`)
		}
		if filepath.IsAbs(entry) {
			dirs = append(dirs, filepath.Clean(entry))
		}
	}
	return dirs
}

func uniquePaths(paths []string, goos string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			continue
		}
		path = filepath.Clean(path)
		key := path
		if goos == "windows" {
			key = strings.ToLower(path)
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, path)
		}
	}
	return out
}

func pathsIn(dirs []string, name, goos string) []string {
	var paths []string
	for _, dir := range dirs {
		paths = append(paths, executableNames(filepath.Join(dir, name), goos)...)
	}
	return paths
}

func executableNames(path, goos string) []string {
	if goos == "windows" && filepath.Ext(path) == "" {
		// Do not use arbitrary PATHEXT entries or Windows' implicit CWD search.
		return []string{path + ".exe", path + ".com", path + ".cmd", path + ".bat"}
	}
	return []string{path}
}
