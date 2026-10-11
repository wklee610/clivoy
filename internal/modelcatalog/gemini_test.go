package modelcatalog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A separate fake CLI keeps these protocol tests independent of other native
// providers. It accepts only the two metadata methods, never a model turn.
func init() {
	mode := os.Getenv("CLIVOY_GEMINI_CATALOG_TEST")
	if mode == "" {
		return
	}
	if mode == "hang" {
		time.Sleep(time.Minute)
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	methods := []string{}
	for scanner.Scan() {
		var request struct {
			ID     int            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		methods = append(methods, request.Method)
		switch request.Method {
		case "initialize":
			if len(methods) != 1 || request.ID != 1 || request.Params["protocolVersion"] != float64(1) || request.Params["clientInfo"] == nil {
				os.Exit(3)
			}
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocolVersion": 1}})
		case "session/new":
			cwd, _ := os.Getwd()
			requestedCWD, _ := request.Params["cwd"].(string)
			cwdInfo, cwdErr := os.Stat(cwd)
			requestedInfo, requestedErr := os.Stat(requestedCWD)
			servers, ok := request.Params["mcpServers"].([]any)
			if len(methods) != 2 || request.ID != 2 || cwdErr != nil || requestedErr != nil || !os.SameFile(cwdInfo, requestedInfo) || !ok || len(servers) != 0 {
				os.Exit(4)
			}
			settingsPath := os.Getenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH")
			settings, err := os.ReadFile(settingsPath)
			if err != nil {
				os.Exit(5)
			}
			info, err := os.Stat(settingsPath)
			if err != nil {
				os.Exit(6)
			}
			var recordedEnv []string
			for _, entry := range os.Environ() {
				name, _, _ := strings.Cut(entry, "=")
				switch strings.ToUpper(name) {
				case "CLIVOY_API_KEY", "CLIVOY_HOME", "GEMINI_TEST_UPSTREAM", "GEMINI_CLI_SYSTEM_SETTINGS_PATH", "GEMINI_CLI_SYSTEM_DEFAULTS_PATH", "GEMINI_CLI_NO_RELAUNCH", "GEMINI_SYSTEM_MD", "GEMINI_WRITE_SYSTEM_MD", "NO_BROWSER", "GEMINI_CLI_IDE_SERVER_STDIO_COMMAND", "GEMINI_CLI_IDE_SERVER_STDIO_ARGS", "GEMINI_CLI_IDE_SERVER_PORT":
					recordedEnv = append(recordedEnv, entry)
				}
			}
			record := map[string]any{
				"methods": methods, "args": os.Args[1:], "cwd": requestedCWD, "settingsPath": settingsPath,
				"settings": json.RawMessage(settings), "settingsMode": uint32(info.Mode().Perm()),
				"home": os.Getenv("HOME"), "environ": recordedEnv,
			}
			data, _ := json.Marshal(record)
			if err := os.WriteFile(os.Getenv("CLIVOY_GEMINI_CATALOG_RECORD"), data, 0600); err != nil {
				os.Exit(7)
			}
			switch mode {
			case "old":
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"sessionId": "metadata-only"}})
			case "auth-error":
				fmt.Fprintln(os.Stderr, "private-cli-credential")
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "error": map[string]any{"code": -32000, "message": "private-cli-credential"}})
			case "malformed":
				fmt.Println("private-cli-credential is not JSON")
			default:
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{}})
				_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{
					"sessionId": "metadata-only", "models": map[string]any{
						"currentModelId":  "never-inject-current",
						"availableModels": []map[string]string{{"modelId": "gemini-live-z", "name": "Live Z"}, {"modelId": "gemini-live-a", "name": "Live A"}, {"modelId": "gemini-live-z"}, {"modelId": "--unsafe"}},
					},
				}})
			}
		default:
			os.Exit(8)
		}
	}
	os.Exit(0)
}

func TestDiscoverGeminiUsesOnlyACPMetadataAndIsolatesIntegrations(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLIVOY_API_KEY", "private-gateway")
	t.Setenv("CLIVOY_HOME", "private-config")
	t.Setenv("GEMINI_TEST_UPSTREAM", "private-upstream")
	t.Setenv("GEMINI_CLI_SYSTEM_SETTINGS_PATH", "untrusted-settings")
	t.Setenv("GEMINI_SYSTEM_MD", "untrusted-memory")
	t.Setenv("GEMINI_CLI_IDE_SERVER_STDIO_COMMAND", "untrusted-command")
	t.Setenv("CLIVOY_GEMINI_CATALOG_TEST", "success")
	recordPath := filepath.Join(t.TempDir(), "record.json")
	t.Setenv("CLIVOY_GEMINI_CATALOG_RECORD", recordPath)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	models, err := DiscoverGemini(context.Background(), binary, "gemini_test_upstream")
	if err != nil {
		t.Fatal(err)
	}
	want := []Model{{ID: "gemini-live-a", Name: "Live A", Capabilities: []string{"chat"}}, {ID: "gemini-live-z", Name: "Live Z", Capabilities: []string{"chat"}}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("CLI catalog = %+v, want %+v", models, want)
	}
	var record struct {
		Methods      []string       `json:"methods"`
		Args         []string       `json:"args"`
		CWD          string         `json:"cwd"`
		SettingsPath string         `json:"settingsPath"`
		SettingsMode uint32         `json:"settingsMode"`
		Settings     map[string]any `json:"settings"`
		Home         string         `json:"home"`
		Environ      []string       `json:"environ"`
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(record.Methods, []string{"initialize", "session/new"}) {
		t.Fatalf("unexpected RPC methods: %v", record.Methods)
	}
	noMCP := filepath.Base(filepath.Dir(record.SettingsPath))
	wantArgs := []string{"--experimental-acp", "--approval-mode", "default", "--extensions", "none", "--allowed-mcp-server-names", noMCP}
	if !reflect.DeepEqual(record.Args, wantArgs) {
		t.Fatalf("CLI args = %v, want %v", record.Args, wantArgs)
	}
	if record.Home != home || record.CWD != filepath.Join(filepath.Dir(record.SettingsPath), "work") {
		t.Fatalf("native home or working directory isolation was lost")
	}
	if runtime.GOOS != "windows" && record.SettingsMode != 0600 {
		t.Fatalf("settings permissions = %o, want 600", record.SettingsMode)
	}
	if _, err := os.Stat(filepath.Dir(record.SettingsPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata temporary directory was not removed: %v", err)
	}
	env := make(map[string]string)
	for _, entry := range record.Environ {
		name, value, _ := strings.Cut(entry, "=")
		env[strings.ToUpper(name)] = value
	}
	for _, name := range []string{"CLIVOY_API_KEY", "CLIVOY_HOME", "GEMINI_TEST_UPSTREAM"} {
		if _, ok := env[name]; ok {
			t.Errorf("excluded environment %s was inherited", name)
		}
	}
	for name, value := range map[string]string{
		"GEMINI_CLI_SYSTEM_SETTINGS_PATH": record.SettingsPath,
		"GEMINI_CLI_SYSTEM_DEFAULTS_PATH": filepath.Join(filepath.Dir(record.SettingsPath), "no-defaults.json"),
		"GEMINI_CLI_NO_RELAUNCH":          "true", "GEMINI_SYSTEM_MD": "false", "GEMINI_WRITE_SYSTEM_MD": "false", "NO_BROWSER": "true",
		"GEMINI_CLI_IDE_SERVER_STDIO_COMMAND": "", "GEMINI_CLI_IDE_SERVER_STDIO_ARGS": "", "GEMINI_CLI_IDE_SERVER_PORT": "",
	} {
		if env[name] != value {
			t.Errorf("isolation environment %s was not applied", name)
		}
	}
	checkSetting := func(group, key string, want any) {
		t.Helper()
		values, ok := record.Settings[group].(map[string]any)
		if !ok || !reflect.DeepEqual(values[key], want) {
			t.Errorf("settings %s.%s = %v, want %v", group, key, values[key], want)
		}
	}
	checkSetting("tools", "core", []any{})
	checkSetting("tools", "discoveryCommand", "")
	checkSetting("tools", "callCommand", "")
	checkSetting("tools", "enableHooks", false)
	checkSetting("hooksConfig", "enabled", false)
	checkSetting("skills", "enabled", false)
	checkSetting("ide", "enabled", false)
	checkSetting("context", "fileName", noMCP+".md")
	checkSetting("context", "includeDirectoryTree", false)
	checkSetting("context", "loadMemoryFromIncludeDirectories", false)
	checkSetting("mcp", "allowed", []any{noMCP})
	checkSetting("mcp", "excluded", []any{noMCP})
	checkSetting("advanced", "ignoreLocalEnv", true)
	checkSetting("experimental", "enableAgents", false)
	checkSetting("experimental", "autoMemory", false)
	checkSetting("general", "enableAutoUpdate", false)
	checkSetting("general", "sessionRetention", map[string]any{"enabled": false})
	checkSetting("telemetry", "enabled", false)
	if _, set := record.Settings["security"]; set {
		t.Fatal("model discovery must not replace native auth selection")
	}
}

func TestDiscoverGeminiFailsWithoutFallbackOrPrivateDiagnostics(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"old", "auth-error", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CLIVOY_GEMINI_CATALOG_TEST", mode)
			t.Setenv("CLIVOY_GEMINI_CATALOG_RECORD", filepath.Join(t.TempDir(), "record.json"))
			models, err := DiscoverGemini(context.Background(), binary)
			if err == nil || len(models) != 0 || strings.Contains(err.Error(), "private-cli-credential") {
				t.Fatalf("failure returned models or private diagnostics: models=%v err=%v", models, err)
			}
			if mode == "old" && !strings.Contains(err.Error(), "update Gemini CLI") {
				t.Fatalf("old CLI error did not explain missing ACP metadata: %v", err)
			}
		})
	}
}

func TestDiscoverGeminiBoundsWaitAndHonorsCancellation(t *testing.T) {
	t.Setenv("CLIVOY_GEMINI_CATALOG_TEST", "hang")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	models, err := DiscoverGemini(ctx, binary)
	if !errors.Is(err, context.DeadlineExceeded) || len(models) != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("unbounded metadata lookup: models=%v elapsed=%s err=%v", models, time.Since(start), err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	if _, err := DiscoverGemini(ctx, "/must-not-run"); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
}

func TestGeminiACPRejectsInvalidOrInteractiveMetadata(t *testing.T) {
	init := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}` + "\n"
	for _, result := range []string{
		`{"jsonrpc":"2.0","id":2,"result":{"models":{"availableModels":[]}}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"models":{"availableModels":[{"modelId":"--unsafe"}]}}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"models":"private-cli-credential"}}`,
		`{"jsonrpc":"2.0","id":99,"method":"session/request_permission","params":{"secret":"private-cli-credential"}}`,
		strings.Repeat(`{"jsonrpc":"2.0","method":"session/update"}`+"\n", 129),
	} {
		var sent bytes.Buffer
		models, err := geminiACPModels(bufio.NewScanner(strings.NewReader(init+result)), json.NewEncoder(&sent), "/private-work")
		if err == nil || len(models) != 0 || strings.Contains(err.Error(), "private-cli-credential") {
			t.Fatalf("unsafe metadata accepted: models=%v err=%v", models, err)
		}
		if strings.Contains(sent.String(), "session/prompt") || strings.Contains(sent.String(), "authenticate") {
			t.Fatal("metadata failure triggered inference or authentication")
		}
	}
	var sent bytes.Buffer
	_, err := geminiACPModels(bufio.NewScanner(strings.NewReader(`{"id":1,"result":{"protocolVersion":999}}`)), json.NewEncoder(&sent), "/private-work")
	if err == nil || strings.Contains(sent.String(), "session/new") {
		t.Fatalf("unsupported protocol initialized a session: %v", err)
	}
}
