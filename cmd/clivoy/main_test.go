package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/wklee610/clivoy/internal/clidiscovery"
	"github.com/wklee610/clivoy/internal/config"
)

func TestInitDoesNotOverwrite(t *testing.T) {
	t.Setenv("CLIVOY_API_KEY", "")
	path := filepath.Join(t.TempDir(), "clivoy.json")
	var out bytes.Buffer
	if err := run([]string{"init", "--output", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.AuthFile != "auth.json" || c.APIKeyFile != "" {
		t.Fatalf("unexpected auth path: %s", c.AuthFile)
	}
	key, err := c.ResolveAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	suffix, ok := strings.CutPrefix(key, "sk-clivoy-")
	if !ok {
		t.Fatal("generated key is missing the Clivoy prefix")
	}
	decoded, err := hex.DecodeString(suffix)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("generated key length=%d err=%v", len(decoded), err)
	}
	if strings.Contains(out.String(), key) {
		t.Fatal("init printed the secret")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(path), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	var authData struct {
		Keys []string `json:"keys"`
	}
	if err := json.Unmarshal(data, &authData); err != nil || len(authData.Keys) != 1 || authData.Keys[0] != key {
		t.Fatal("init did not create the auth.json key list")
	}
	if _, err := os.Lstat(path + ".key"); !os.IsNotExist(err) {
		t.Fatal("init created a legacy plain-text key file")
	}
	if !strings.Contains(out.String(), "start --config "+strconv.Quote(path)) {
		t.Fatal("init did not explain how to start the configured server")
	}
	if runtime.GOOS != "windows" {
		for _, filePath := range []string{path, filepath.Join(filepath.Dir(path), "auth.json")} {
			info, err := os.Stat(filePath)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("file permission for %s: %v, %v", filePath, info, err)
			}
		}
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"init", "--output", path}, &out, &out); err == nil {
		t.Fatal("overwrote existing config")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("existing config changed")
	}
	if afterKey, err := c.ResolveAPIKey(); err != nil || afterKey != key {
		t.Fatal("existing key changed")
	}
}

func TestInitPreservesExistingKeyAndRollsBackNewConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clivoy.json")
	authPath := filepath.Join(filepath.Dir(path), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"keys":["existing-secret"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"init", "--output", path}, &out, &out); err == nil {
		t.Fatal("init overwrote an existing key")
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("failed init left a config behind: %v", err)
	}
	if data, err := os.ReadFile(authPath); err != nil || string(data) != `{"keys":["existing-secret"]}` {
		t.Fatal("pre-existing key was changed or removed")
	}
}

func TestInitRejectsSymlinks(t *testing.T) {
	for _, targetKind := range []string{"config", "key"} {
		t.Run(targetKind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "clivoy.json")
			target := path + ".target"
			if err := os.WriteFile(target, []byte("do-not-change"), 0600); err != nil {
				t.Fatal(err)
			}
			link := path
			if targetKind == "key" {
				link = filepath.Join(filepath.Dir(path), "auth.json")
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			var out bytes.Buffer
			if err := run([]string{"init", "--output", path}, &out, &out); err == nil {
				t.Fatal("init accepted existing symlink")
			}
			if data, err := os.ReadFile(target); err != nil || string(data) != "do-not-change" {
				t.Fatal("symlink target changed")
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatal("existing symlink was removed")
			}
		})
	}
}

func TestRollbackPreservesReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("created"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	removeCreatedFile(path, info)
	if data, err := os.ReadFile(path); err != nil || string(data) != "replacement" {
		t.Fatal("rollback removed a replacement file")
	}
}

func TestInitSavesDiscoveredCLICommand(t *testing.T) {
	dir := t.TempDir()
	name := "claude"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	command := filepath.Join(dir, name)
	if err := os.WriteFile(command, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	path := filepath.Join(t.TempDir(), "clivoy.json")
	var out bytes.Buffer
	if err := run([]string{"init", "--output", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers["claude"].Command != command {
		t.Fatalf("init did not save the detected CLI path: %q", c.Providers["claude"].Command)
	}
}

func TestDiscoveredCommandsPreserveOverridesAndCLIEntrypoints(t *testing.T) {
	c := config.Default()
	c.Providers["codex"] = config.Provider{Type: "codex", Command: "/custom/codex"}
	c.Providers["http"] = config.Provider{Type: "openai", BaseURL: "http://localhost/v1"}
	setDiscoveredCLICommands(&c, func(kind, command string) (clidiscovery.Resolution, error) {
		if kind == "codex" || kind == "ollama" || kind == "openai" || command != "" {
			t.Fatalf("discovery attempted for an explicit command or HTTP backend: %s %s", kind, command)
		}
		if kind == "grok" {
			return clidiscovery.Resolution{}, errors.New("not installed")
		}
		return clidiscovery.Resolution{
			Path: "/installed/node", Args: []string{"/installed/" + kind + ".js"},
			Command: "/installed/" + kind,
		}, nil
	})
	if c.Providers["codex"].Command != "/custom/codex" {
		t.Fatal("explicit command was changed")
	}
	for _, name := range []string{"claude", "gemini"} {
		if c.Providers[name].Command != "/installed/"+name {
			t.Fatalf("%s saved the interpreter instead of the CLI entrypoint", name)
		}
	}
	if c.Providers["grok"].Command != "" {
		t.Fatal("missing CLI acquired a command")
	}
}

func TestInitMissingCLIGuidance(t *testing.T) {
	c := config.Default()
	c.Providers["http"] = config.Provider{Type: "openai", BaseURL: "http://localhost/v1"}
	path := filepath.Join(t.TempDir(), "custom.json")
	var out bytes.Buffer
	var called []string
	reportMissingCLIs(c, path, &out, func(kind, command string) (clidiscovery.Resolution, error) {
		called = append(called, kind)
		if kind == "claude" || kind == "gemini" || kind == "grok" {
			return clidiscovery.Resolution{}, errors.New("not found")
		}
		return clidiscovery.Resolution{Path: "/installed/codex"}, nil
	})
	if len(called) != 4 || !strings.Contains(out.String(), "providers.claude.command") || !strings.Contains(out.String(), "providers.gemini.command") || !strings.Contains(out.String(), "providers.grok.command") || !strings.Contains(out.String(), strconv.Quote(path)) || !strings.Contains(out.String(), "absolute path") {
		t.Fatal("missing CLI guidance did not identify the correct config field and file")
	}
	if strings.Contains(out.String(), "providers.codex.command") || strings.Contains(out.String(), "http") {
		t.Fatal("init reported guidance for an installed CLI or HTTP provider")
	}
}

func TestAuthAndDoctorWithGeneratedFileKey(t *testing.T) {
	t.Setenv("CLIVOY_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("CLIVOY_API_KEY", "")
	path := filepath.Join(t.TempDir(), "clivoy.json")
	var out bytes.Buffer
	if err := run([]string{"init", "--output", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	key, err := c.ResolveAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	// The shell-friendly auth command prints only the first active key.
	authData, err := json.Marshal(map[string][]string{"keys": {key, "second-test-key"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "auth.json"), authData, 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"auth", "--config", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != key+"\n" {
		t.Fatal("auth did not print precisely the configured key")
	}
	out.Reset()
	if err := run([]string{"doctor", "--config", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), key) || strings.Contains(out.String(), "second-test-key") || !strings.Contains(out.String(), "API key: configured") {
		t.Fatalf("unexpected doctor output: %s", out.String())
	}
	out.Reset()
	// An invalid port stops before listening, after the file key has been
	// resolved and passed to the server without requiring a process signal.
	err = run([]string{"serve", "--config", path, "--listen", "127.0.0.1:notaport"}, &out, &out)
	if err == nil || strings.Contains(err.Error(), "API key") {
		t.Fatalf("serve failed to use generated key: %v", err)
	}
	if strings.Contains(out.String(), key) || strings.Contains(err.Error(), key) {
		t.Fatal("startup exposed the key")
	}
	if err := os.Remove(filepath.Join(filepath.Dir(path), "auth.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"doctor", "--config", path}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "API key: missing or invalid") {
		t.Fatal("doctor did not identify unavailable file key")
	}
}

func TestServeRequiresKey(t *testing.T) {
	t.Setenv("CLIVOY_API_KEY", "")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"cli":{"type":"codex"}},"models":{"codex":{"provider":"cli"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"serve", "--config", path}, &out, &out); err == nil || !strings.Contains(err.Error(), "CLIVOY_API_KEY") {
		t.Fatalf("expected missing key error, got %v", err)
	}
}

func TestDoctorDoesNotExposeKey(t *testing.T) {
	t.Setenv("CLIVOY_HOME", filepath.Join(t.TempDir(), "home"))
	t.Setenv("CLIVOY_API_KEY", "test-secret-that-must-not-be-printed")
	var out bytes.Buffer
	if err := run([]string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"doctor"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), os.Getenv("CLIVOY_API_KEY")) {
		t.Fatal("doctor printed secret")
	}
}

func TestDefaultHomeInitializationAndWorkingDirectoryIndependence(t *testing.T) {
	home := filepath.Join(t.TempDir(), "clivoy data")
	t.Setenv("CLIVOY_HOME", home)
	t.Setenv("CLIVOY_API_KEY", "")
	t.Chdir(t.TempDir())
	var out bytes.Buffer
	if err := run([]string{"init"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	c, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	key, err := c.ResolveAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), key) {
		t.Fatal("init leaked the key")
	}
	for _, dir := range []string{home, filepath.Join(home, "logs")} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("missing state directory %s: %v", dir, err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0700 {
			t.Fatalf("state directory permissions: %s", info.Mode())
		}
	}
	if _, err := os.Lstat(filepath.Join(home, "tmp")); !os.IsNotExist(err) {
		t.Fatalf("init created a persistent temporary directory: %v", err)
	}
	if _, err := os.Stat("clivoy.json"); !os.IsNotExist(err) {
		t.Fatal("default init wrote to the working directory")
	}
	t.Chdir(t.TempDir())
	out.Reset()
	if err := run([]string{"auth"}, &out, &out); err != nil || out.String() != key+"\n" {
		t.Fatalf("default auth failed outside init directory: %v", err)
	}
	out.Reset()
	if err := run([]string{"doctor"}, &out, &out); err != nil || !strings.Contains(out.String(), "API key: configured") {
		t.Fatalf("default doctor failed: %v", err)
	}
	if strings.Contains(out.String(), key) {
		t.Fatal("doctor leaked the key")
	}
}

func TestFirstServeInitializesHomeAndWritesLogWithoutLeakingKey(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("CLIVOY_HOME", home)
	t.Setenv("CLIVOY_API_KEY", "")
	var out bytes.Buffer
	err := run([]string{"serve", "--listen", "127.0.0.1:notaport"}, &out, &out)
	if err == nil {
		t.Fatal("invalid port accepted")
	}
	c, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	key, err := c.ResolveAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(home, "logs", "clivoy.log")
	log, err := os.ReadFile(logPath)
	if err != nil || !bytes.Contains(log, []byte("server failed")) {
		t.Fatalf("startup failure not logged: %v", err)
	}
	if strings.Contains(out.String(), key) || bytes.Contains(log, []byte(key)) {
		t.Fatal("startup/log exposed the key")
	}
	if _, err := os.Lstat(filepath.Join(home, "tmp")); !os.IsNotExist(err) {
		t.Fatalf("serve created a persistent temporary directory: %v", err)
	}
	if err := run([]string{"serve", "--listen", "127.0.0.1:notaport"}, &out, &out); err == nil {
		t.Fatal("invalid port accepted on restart")
	}
	if nextKey, err := c.ResolveAPIKey(); err != nil || nextKey != key {
		t.Fatal("restart regenerated the existing key")
	}
	if err := run([]string{"init"}, &out, &out); err == nil {
		t.Fatal("explicit init overwrote initialized home")
	}
}

func TestMissingExplicitConfigDoesNotInitializeDefaultHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("CLIVOY_HOME", home)
	var out bytes.Buffer
	if err := run([]string{"serve", "--config", filepath.Join(t.TempDir(), "missing.json")}, &out, &out); err == nil {
		t.Fatal("missing explicit config accepted")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("explicit config request created default state")
	}
}

func TestRuntimeStorageRespectsCustomLogAndReadOnlyContainerMode(t *testing.T) {
	// Explicit configurations with either CLI or HTTP providers can disable file
	// logging without needing a valid state-home setting.
	for _, kind := range []string{"cli", "http"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("CLIVOY_HOME", "invalid-relative-home")
			c := config.Default()
			if kind == "http" {
				c.Providers = map[string]config.Provider{"http": {Type: "openai", BaseURL: "http://localhost/v1"}}
			}
			c.LogFile = "-"
			if writer, err := runtimeStorage(c); err != nil || writer != nil {
				t.Fatalf("console-only configuration touched state: %v", err)
			}
			c.LogFile = filepath.Join(t.TempDir(), "separate", "server.log")
			writer, err := runtimeStorage(c)
			if err != nil {
				t.Fatal(err)
			}
			if writer == nil {
				t.Fatal("missing custom log writer")
			}
			if _, err := writer.Write([]byte("custom log\n")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if data, err := os.ReadFile(c.LogFile); err != nil || string(data) != "custom log\n" {
				t.Fatalf("custom log not written: %v", err)
			}
		})
	}
}

func TestKeygen(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"keygen"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	suffix, ok := strings.CutPrefix(strings.TrimSpace(out.String()), "sk-clivoy-")
	if !ok {
		t.Fatal("generated key is missing the Clivoy prefix")
	}
	key, err := hex.DecodeString(suffix)
	if err != nil || len(key) != 32 {
		t.Fatalf("invalid generated key: length=%d error=%v", len(key), err)
	}
}
