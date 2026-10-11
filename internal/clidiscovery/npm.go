package clidiscovery

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func (r resolver) npmWrapper(kind, command string) (Resolution, error) {
	entry := `node_modules\@openai\codex\bin\codex.js`
	if kind == "claude" {
		entry = `node_modules\@anthropic-ai\claude-code\cli.js`
	} else if kind == "gemini" {
		entry = `node_modules\@google\gemini-cli\dist\index.js`
	} else if kind == "grok" {
		entry = `node_modules\@xai-official\grok\bin\grok`
	}
	f, err := os.Open(command)
	if err != nil {
		return Resolution{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 32*1024+1))
	if err != nil {
		return Resolution{}, err
	}
	if len(data) > 32*1024 || !isNPMNodeWrapper(string(data), entry) {
		return Resolution{}, errors.New("unsupported Windows command wrapper; configure a native executable or an unmodified npm CLI .cmd entrypoint")
	}
	script := filepath.Join(filepath.Dir(command), filepath.FromSlash(strings.ReplaceAll(entry, `\`, "/")))
	result, err := r.nodeScript(command, script)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve npm wrapper: %w", err)
	}
	return result, nil
}

// Recognize only npm cmd-shim's Node launch/control statements. Extra commands,
// environment assignments, script paths, and Node flags are rejected rather
// than silently bypassing an administrator's custom wrapper behavior.
// The recognized wrapper is never interpreted by cmd.exe or PowerShell.
func isNPMNodeWrapper(text, entry string) bool {
	called := false
	for _, raw := range strings.Split(strings.TrimPrefix(text, "\ufeff"), "\n") {
		line := strings.ToLower(strings.Join(strings.Fields(raw), " "))
		if line == "" {
			continue
		}
		switch line {
		case "@echo off", "goto start", ":find_dp0", "set dp0=%~dp0", "exit /b", ":start", "setlocal", "@setlocal", "call :find_dp0", ") else (", ")", "@set pathext=%pathext:;.js;=;%":
			continue
		}
		matched := false
		for _, base := range []string{`%dp0%\`, `%~dp0\`} {
			node := `"` + base + `node.exe"`
			target := `"` + base + strings.ToLower(entry) + `"`
			switch line {
			case "if exist " + node + " (", "@if exist " + node + " (",
				`set "_prog=` + base + `node.exe"`, `set "_prog=node"`:
				matched = true
			case node + " " + target + " %*", "node " + target + " %*":
				matched, called = true, true
			case `endlocal & goto #_undefined_# 2>nul || title %comspec% & set pathext=%pathext:;.js;=;% & "%_prog%" ` + target + " %*":
				matched, called = true, true
			}
		}
		if !matched {
			return false
		}
	}
	return called
}
