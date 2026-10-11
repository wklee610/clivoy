package clidiscovery

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Only named installation roots and one directory level of versioned Node
// installations are inspected. Cache trees and shell profiles are not read.
func installationLocations(goos, home string, getenv func(string) string) ([]string, []string) {
	var dirs, apps []string
	add := func(root string, parts ...string) {
		if filepath.IsAbs(root) {
			dirs = append(dirs, filepath.Join(append([]string{root}, parts...)...))
		}
	}
	versions := func(root string, parts ...string) {
		if filepath.IsAbs(root) {
			dirs = append(dirs, versionDirectories(root, parts...)...)
		}
	}
	add(home, ".local", "bin")
	add(home, "bin")
	add(home, ".grok", "bin")
	add(getenv("VOLTA_HOME"), "bin")
	add(home, ".volta", "bin")
	if goos == "windows" {
		add(getenv("APPDATA"), "npm")
		add(getenv("LOCALAPPDATA"), "Volta", "bin")
		add(getenv("LOCALAPPDATA"), "Programs", "nodejs")
		add(getenv("ProgramFiles"), "nodejs")
		add(getenv("ProgramW6432"), "nodejs")
		add(getenv("NVM_SYMLINK"))
		for _, key := range []string{"NPM_CONFIG_PREFIX", "npm_config_prefix"} {
			add(getenv(key))
		}
		versions(getenv("NVM_HOME"))
	} else {
		add(getenv("HOMEBREW_PREFIX"), "bin")
		add(home, ".npm-global", "bin")
		add(home, ".npm", "bin")
		for _, key := range []string{"NPM_CONFIG_PREFIX", "npm_config_prefix"} {
			add(getenv(key), "bin")
		}
		add("/opt/homebrew/bin")
		add("/usr/local/bin")
		if goos == "linux" {
			add("/home/linuxbrew/.linuxbrew/bin")
			add(home, ".linuxbrew", "bin")
		}
		add("/usr/bin")
		add("/bin")
	}
	add(getenv("NVM_BIN"))
	for _, root := range []string{getenv("NVM_DIR"), homePath(home, ".nvm")} {
		if filepath.IsAbs(root) {
			versions(filepath.Join(root, "versions", "node"), "bin")
		}
	}
	add(getenv("FNM_MULTISHELL_PATH"), "bin")
	if goos == "windows" {
		add(getenv("FNM_MULTISHELL_PATH"))
	}
	fnmRoots := []string{getenv("FNM_DIR"), homePath(getenv("XDG_DATA_HOME"), "fnm"), homePath(home, ".local", "share", "fnm"), homePath(home, "Library", "Application Support", "fnm")}
	if goos == "windows" {
		fnmRoots = append(fnmRoots, homePath(getenv("LOCALAPPDATA"), "fnm"), homePath(getenv("APPDATA"), "fnm"))
	}
	for _, root := range fnmRoots {
		if filepath.IsAbs(root) {
			if goos == "windows" {
				versions(filepath.Join(root, "node-versions"), "installation")
			} else {
				versions(filepath.Join(root, "node-versions"), "installation", "bin")
			}
		}
	}
	for _, root := range []string{getenv("ASDF_DATA_DIR"), homePath(home, ".asdf")} {
		if filepath.IsAbs(root) {
			versions(filepath.Join(root, "installs", "nodejs"), "bin")
		}
	}
	for _, root := range []string{getenv("MISE_DATA_DIR"), homePath(getenv("XDG_DATA_HOME"), "mise"), homePath(home, ".local", "share", "mise"), homePath(getenv("LOCALAPPDATA"), "mise")} {
		if filepath.IsAbs(root) {
			if goos == "windows" {
				versions(filepath.Join(root, "installs", "node"))
			} else {
				versions(filepath.Join(root, "installs", "node"), "bin")
			}
		}
	}
	for _, root := range []string{getenv("VOLTA_HOME"), homePath(home, ".volta")} {
		if filepath.IsAbs(root) {
			versions(filepath.Join(root, "tools", "image", "node"), "bin")
		}
	}
	if goos == "darwin" {
		for _, applications := range []string{"/Applications", homePath(home, "Applications")} {
			if !filepath.IsAbs(applications) {
				continue
			}
			for _, bundle := range []string{"ChatGPT.app", "Codex.app"} {
				for _, executable := range [][]string{
					{"Contents", "Resources", "codex-cli", "CodexCLI.app", "Contents", "MacOS", "codex"},
					{"Contents", "Resources", "codex"},
					{"Contents", "Resources", "codex-cli", "codex"},
				} {
					parts := append([]string{applications, bundle}, executable...)
					apps = append(apps, filepath.Join(parts...))
				}
			}
		}
	}
	return uniquePaths(dirs, goos), uniquePaths(apps, goos)
}

func homePath(home string, parts ...string) string {
	if !filepath.IsAbs(home) {
		return ""
	}
	return filepath.Join(append([]string{home}, parts...)...)
}

func versionDirectories(root string, suffix ...string) []string {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil
	}
	f, err := os.Open(root)
	if err != nil {
		return nil
	}
	defer f.Close()
	// Refuse an unusually large tree instead of enumerating a cache or home.
	entries, _ := f.ReadDir(129)
	if len(entries) > 128 {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && isNodeVersion(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Slice(names, func(i, j int) bool { return versionGreater(names[i], names[j]) })
	var dirs []string
	for _, name := range names {
		dirs = append(dirs, filepath.Join(append([]string{root, name}, suffix...)...))
	}
	return dirs
}

func isNodeVersion(name string) bool {
	parts := strings.Split(strings.TrimPrefix(name, "v"), ".")
	if len(parts) < 1 || len(parts) > 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

func versionGreater(a, b string) bool {
	aa, bb := strings.Split(strings.TrimPrefix(a, "v"), "."), strings.Split(strings.TrimPrefix(b, "v"), ".")
	for i := 0; i < 3; i++ {
		var av, bv uint64
		if i < len(aa) {
			av, _ = strconv.ParseUint(aa[i], 10, 64)
		}
		if i < len(bb) {
			bv, _ = strconv.ParseUint(bb[i], 10, 64)
		}
		if av != bv {
			return av > bv
		}
	}
	return a > b
}
