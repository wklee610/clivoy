package clidiscovery

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestKnownLocationsAndVersionOrder(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, ".nvm", "versions", "node")
	for _, name := range []string{"v9.1.0", "v22.2.0", "v22.10.0", "not-a-version"} {
		if err := os.MkdirAll(filepath.Join(root, name, "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got := versionDirectories(root, "bin")
	want := []string{filepath.Join(root, "v22.10.0", "bin"), filepath.Join(root, "v22.2.0", "bin"), filepath.Join(root, "v9.1.0", "bin")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("version order: %#v", got)
	}
	dirs, apps := installationLocations("darwin", home, func(string) string { return "" })
	contains := func(paths []string, want string) bool {
		for _, path := range paths {
			if path == want {
				return true
			}
		}
		return false
	}
	if !contains(dirs, want[0]) || !contains(dirs, filepath.Join(home, ".local", "bin")) {
		t.Fatalf("missing common directories: %#v", dirs)
	}
	for _, prefix := range []string{"/Applications", filepath.Join(home, "Applications")} {
		for _, bundle := range []string{"ChatGPT.app", "Codex.app"} {
			app := filepath.Join(prefix, bundle, "Contents", "Resources", "codex-cli", "CodexCLI.app", "Contents", "MacOS", "codex")
			if filepath.IsAbs(app) && !contains(apps, app) {
				t.Fatalf("missing app fallback: %s", app)
			}
		}
	}
}

func TestVersionEnumerationIsBounded(t *testing.T) {
	root := t.TempDir()
	for i := range 129 {
		if err := os.Mkdir(filepath.Join(root, "v"+strconv.Itoa(i)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got := versionDirectories(root, "bin"); len(got) != 0 {
		t.Fatalf("oversized version directory was enumerated: %d", len(got))
	}
}

func TestLinuxbrewAndConfiguredPrefix(t *testing.T) {
	home := t.TempDir()
	prefix := filepath.Join(home, "custom-homebrew")
	dirs, _ := installationLocations("linux", home, func(key string) string {
		if key == "HOMEBREW_PREFIX" {
			return prefix
		}
		return ""
	})
	for _, want := range []string{filepath.Join(prefix, "bin"), filepath.Join(home, ".linuxbrew", "bin"), "/home/linuxbrew/.linuxbrew/bin"} {
		if !filepath.IsAbs(want) {
			continue // Unix system path when this test runs on Windows.
		}
		found := false
		for _, dir := range dirs {
			found = found || dir == want
		}
		if !found {
			t.Errorf("missing Linuxbrew directory %s", want)
		}
	}
}
