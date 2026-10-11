package clidiscovery

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func executable(t *testing.T, path, contents string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func nativeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func TestExplicitCommandIsAuthoritative(t *testing.T) {
	dir := t.TempDir()
	preferred := executable(t, filepath.Join(dir, "path", nativeName("codex")), "native")
	fallback := executable(t, filepath.Join(dir, "fallback", nativeName("codex")), "native")
	explicit := executable(t, filepath.Join(dir, nativeName("chosen")), "native")
	r := resolver{goos: runtime.GOOS, path: filepath.Dir(preferred), dirs: []string{filepath.Dir(fallback)}}
	got, err := r.resolve("codex", explicit)
	if err != nil || got.Path != explicit || got.Command != explicit {
		t.Fatalf("explicit resolution: %+v, %v", got, err)
	}
	if _, err := r.resolve("codex", filepath.Join(dir, "missing")); err == nil || !strings.Contains(err.Error(), "automatic fallback is disabled") {
		t.Fatalf("missing explicit command should not fall back: %v", err)
	}
	got, err = r.resolve("codex", "codex")
	if err != nil || got.Path != preferred {
		t.Fatalf("explicit PATH name: %+v, %v", got, err)
	}
	r.path = ""
	if _, err := r.resolve("codex", "codex"); err == nil {
		t.Fatal("explicit PATH name fell back to an installation directory")
	}
}

func TestPATHBeforeInstallAndAppFallback(t *testing.T) {
	dir := t.TempDir()
	pathCLI := executable(t, filepath.Join(dir, "path", nativeName("codex")), "native")
	installed := executable(t, filepath.Join(dir, "install", nativeName("codex")), "native")
	app := executable(t, filepath.Join(dir, "ChatGPT.app", "Contents", "Resources", "codex-cli", "CodexCLI.app", "Contents", "MacOS", nativeName("codex")), "native")
	r := resolver{goos: runtime.GOOS, path: filepath.Dir(pathCLI), dirs: []string{filepath.Dir(installed)}, apps: []string{app}}
	for _, want := range []string{pathCLI, installed, app} {
		got, err := r.resolve("codex", "")
		if err != nil || got.Path != want || !filepath.IsAbs(got.Command) {
			t.Fatalf("want %s, got %+v, %v", want, got, err)
		}
		if err := os.Remove(want); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.resolve("codex", ""); err == nil {
		t.Fatal("missing installations accepted")
	}
}

func TestRelativePATHCannotSelectRequestDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	executable(t, filepath.Join(dir, nativeName("codex")), "untrusted cwd")
	executable(t, filepath.Join(dir, "relative", nativeName("codex")), "untrusted relative")
	trusted := executable(t, filepath.Join(dir, "trusted", nativeName("codex")), "native")
	r := resolver{goos: runtime.GOOS, path: strings.Join([]string{"", ".", "relative"}, pathSeparator(runtime.GOOS)), dirs: []string{filepath.Dir(trusted)}}
	got, err := r.resolve("codex", "")
	if err != nil || got.Path != trusted {
		t.Fatalf("relative PATH resolution: %+v, %v", got, err)
	}
	// A configured relative path is an intentional user override.
	got, err = r.resolve("codex", filepath.Join(".", "relative", nativeName("codex")))
	if err != nil || got.Path != filepath.Join(dir, "relative", nativeName("codex")) {
		t.Fatalf("configured relative path: %+v, %v", got, err)
	}
}

func TestNodeEntrypointUsesPairedNodeWithoutPATH(t *testing.T) {
	dir := t.TempDir()
	script := executable(t, filepath.Join(dir, "prefix", "lib", "node_modules", "@openai", "codex", "bin", "codex.js"), "#!/usr/bin/env node\n")
	node := executable(t, filepath.Join(dir, "prefix", "bin", nativeName("node")), "native")
	node, err := filepath.EvalSymlinks(node)
	if err != nil {
		t.Fatal(err)
	}
	r := resolver{goos: runtime.GOOS}
	got, err := r.resolve("codex", script)
	if err != nil || got.Path != node || got.Command != script || !reflect.DeepEqual(got.Args, []string{script}) {
		t.Fatalf("paired Node resolution: %+v, %v", got, err)
	}
	sep := pathSeparator(runtime.GOOS)
	base := []string{"KEEP=value", "PATH=." + sep + filepath.Join(dir, "tools"), "ANOTHER=unchanged"}
	before := append([]string(nil), base...)
	gotEnv := got.Environment(base)
	wantEnv := []string{"KEEP=value", "ANOTHER=unchanged", "PATH=" + filepath.Dir(node) + sep + filepath.Join(dir, "tools")}
	if !reflect.DeepEqual(gotEnv, wantEnv) || !reflect.DeepEqual(base, before) {
		t.Fatalf("environment: %#v (base %#v)", gotEnv, base)
	}
	if err := os.Remove(node); err != nil {
		t.Fatal(err)
	}
	if _, err := r.resolve("codex", script); err == nil || !strings.Contains(err.Error(), "needs Node.js") {
		t.Fatalf("missing Node: %v", err)
	}
}

func TestNativeClaudeSymlink(t *testing.T) {
	dir := t.TempDir()
	target := executable(t, filepath.Join(dir, "versions", nativeName("claude-native")), "native")
	link := filepath.Join(dir, nativeName("claude"))
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	got, err := (resolver{goos: runtime.GOOS, dirs: []string{dir}}).resolve("claude", "")
	if err != nil || got.Path != link || len(got.Args) != 0 {
		t.Fatalf("native symlink: %+v, %v", got, err)
	}
}

func TestUnixNPMSymlinkUsesPairedNode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable symlink and shebang behavior")
	}
	for _, shebang := range []string{"#!/usr/bin/env node", "#!/usr/bin/env -S node"} {
		t.Run(shebang, func(t *testing.T) {
			dir := t.TempDir()
			script := executable(t, filepath.Join(dir, "lib", "node_modules", "@openai", "codex", "bin", "codex.js"), shebang+"\n")
			node := executable(t, filepath.Join(dir, "bin", "node"), "native")
			link := filepath.Join(dir, "bin", "codex")
			if err := os.Symlink(script, link); err != nil {
				t.Fatal(err)
			}
			got, err := (resolver{goos: runtime.GOOS, dirs: []string{filepath.Dir(link)}}).resolve("codex", "")
			if err != nil || got.Path != node || got.Command != link || !reflect.DeepEqual(got.Args, []string{link}) {
				t.Fatalf("npm symlink: %+v, %v", got, err)
			}
		})
	}
}

func TestRejectsDirectoryAndNonExecutable(t *testing.T) {
	dir := t.TempDir()
	r := resolver{goos: runtime.GOOS}
	if _, err := r.resolve("codex", dir); err == nil {
		t.Fatal("accepted a directory")
	}
	if runtime.GOOS != "windows" {
		file := executable(t, filepath.Join(dir, "codex"), "native")
		if err := os.Chmod(file, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.resolve("codex", file); err == nil || !strings.Contains(err.Error(), "not executable") {
			t.Fatalf("accepted non-executable: %v", err)
		}
	}
}

func TestWindowsNPMWrapperUsesNodeDirectly(t *testing.T) {
	for _, kind := range []string{"codex", "claude", "gemini", "grok"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			entry := `node_modules\@openai\codex\bin\codex.js`
			if kind == "claude" {
				entry = `node_modules\@anthropic-ai\claude-code\cli.js`
			} else if kind == "gemini" {
				entry = `node_modules\@google\gemini-cli\dist\index.js`
			} else if kind == "grok" {
				entry = `node_modules\@xai-official\grok\bin\grok`
			}
			wrapper := executable(t, filepath.Join(dir, kind+".cmd"), "@ECHO off\r\nSETLOCAL\r\n"+`node "%~dp0\`+entry+`" %*`+"\r\n")
			script := executable(t, filepath.Join(dir, filepath.FromSlash(strings.ReplaceAll(entry, `\`, "/"))), "#!/usr/bin/env node\n")
			node := executable(t, filepath.Join(dir, "node.exe"), "native")
			got, err := (resolver{goos: "windows", path: ".;" + dir}).resolve(kind, "")
			if err != nil || got.Path != node || got.Command != wrapper || !reflect.DeepEqual(got.Args, []string{script}) {
				t.Fatalf("npm wrapper: %+v, %v", got, err)
			}
			if err := os.WriteFile(wrapper, []byte("@echo off\r\nECHO custom hook\r\n"+`node "%~dp0\`+entry+`" %*`), 0o755); err != nil {
				t.Fatal(err)
			}
			if _, err := (resolver{goos: "windows", dirs: []string{dir}}).resolve(kind, wrapper); err == nil || !strings.Contains(err.Error(), "unsupported Windows command wrapper") {
				t.Fatalf("custom wrapper must fail explicitly: %v", err)
			}
		})
	}
}

func TestModernNPMWrapperRecognition(t *testing.T) {
	entry := `node_modules\@openai\codex\bin\codex.js`
	wrapper := "@ECHO off\nGOTO start\n:find_dp0\nSET dp0=%~dp0\nEXIT /b\n:start\nSETLOCAL\nCALL :find_dp0\n" +
		`IF EXIST "%dp0%\node.exe" (` + "\n" + `  SET "_prog=%dp0%\node.exe"` + "\n) ELSE (\n" + `  SET "_prog=node"` + "\n)\n" +
		`endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & set PATHEXT=%PATHEXT:;.JS;=;% & "%_prog%"  "%dp0%\` + entry + `" %*`
	if !isNPMNodeWrapper(wrapper, entry) {
		t.Fatal("modern npm wrapper not recognized")
	}
	if isNPMNodeWrapper(strings.ReplaceAll(wrapper, "codex.js", "other.js"), entry) {
		t.Fatal("accepted unexpected npm entrypoint")
	}
}

func TestWindowsEnvironmentCaseAndNativePrecedence(t *testing.T) {
	dir := t.TempDir()
	cli := executable(t, filepath.Join(dir, "codex.exe"), "native")
	executable(t, filepath.Join(dir, "codex.cmd"), "invalid wrapper")
	got, err := (resolver{goos: "windows", path: dir}).resolve("codex", "")
	if err != nil || got.Path != cli {
		t.Fatalf("native precedence: %+v, %v", got, err)
	}
	env := got.Environment([]string{"Path=.;relative;" + dir, "KEEP=yes"})
	if !reflect.DeepEqual(env, []string{"KEEP=yes", "PATH=" + dir}) {
		t.Fatalf("Windows environment: %#v", env)
	}
}
