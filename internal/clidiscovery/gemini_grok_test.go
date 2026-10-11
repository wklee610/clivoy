package clidiscovery

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGeminiAndGrokDiscovery(t *testing.T) {
	for _, kind := range []string{"gemini", "grok"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			pathCLI := executable(t, filepath.Join(dir, "path", nativeName(kind)), "native")
			installed := executable(t, filepath.Join(dir, "installed", nativeName(kind)), "native")
			r := resolver{goos: runtime.GOOS, path: filepath.Dir(pathCLI), dirs: []string{filepath.Dir(installed)}}
			got, err := r.resolve(kind, "")
			if err != nil || got.Path != pathCLI {
				t.Fatalf("PATH discovery: %+v, %v", got, err)
			}
			r.path = ""
			got, err = r.resolve(kind, "")
			if err != nil || got.Path != installed {
				t.Fatalf("installation discovery: %+v, %v", got, err)
			}
			if _, err := r.resolve(kind, "missing"); err == nil || !strings.Contains(err.Error(), "automatic fallback is disabled") {
				t.Fatalf("configured command fell back: %v", err)
			}
		})
	}
}

func TestGrokRejectsUnverifiedNPMWrapper(t *testing.T) {
	dir := t.TempDir()
	path := executable(t, filepath.Join(dir, "grok.cmd"), `node "%~dp0\node_modules\grok\cli.js" %*`)
	if _, err := (resolver{goos: "windows"}).resolve("grok", path); err == nil || !strings.Contains(err.Error(), "unsupported Windows command wrapper") {
		t.Fatalf("accepted unsupported Grok wrapper: %v", err)
	}
}
