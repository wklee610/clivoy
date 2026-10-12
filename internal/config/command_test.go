package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadCLICommands(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		want          Command
	}{
		{"codex arguments", `["codex","--yolo"]`, Command{"codex", "--yolo"}},
		{"claude arguments", `["claude","--dangerously-skip-permissions"]`, Command{"claude", "--dangerously-skip-permissions"}},
		{"literal arguments", `["/path with spaces/codex","--config","value with spaces; $(literal)",""]`, Command{"/path with spaces/codex", "--config", "value with spaces; $(literal)", ""}},
		{"legacy executable", `"/path with spaces/codex"`, Command{"/path with spaces/codex"}},
		{"legacy string is not split", `"codex --yolo"`, Command{"codex --yolo"}},
		{"legacy empty discovers", `""`, nil},
		{"null discovers", `null`, nil},
		{"empty array discovers", `[]`, Command{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "clivoy.json")
			body := `{"providers":{"local":{"type":"codex","command":` + tc.command + `}}}`
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			got := c.Providers["local"].Command
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("command = %#v, want %#v", got, tc.want)
			}
			wantExecutable := ""
			if len(tc.want) > 0 {
				wantExecutable = tc.want[0]
			}
			if got.Executable() != wantExecutable {
				t.Fatalf("executable = %q, want %q", got.Executable(), wantExecutable)
			}
			if len(got) > 0 {
				encoded, err := json.Marshal(got)
				if err != nil || len(encoded) == 0 || encoded[0] != '[' {
					t.Fatalf("command must marshal as an array: %s, %v", encoded, err)
				}
			}
		})
	}
}

func TestLoadRejectsInvalidCLICommands(t *testing.T) {
	for _, command := range []string{
		`true`, `42`, `{}`, `["codex",null]`, `[null]`, `["codex",42]`,
		`["codex",true]`, `["codex",{}]`, `["codex",[]]`, `[""]`,
		`[" \t"]`, `" \t"`, `["co\u0000dex"]`, `["codex","--\u0000yolo"]`, `"codex\u0000"`,
	} {
		t.Run(command, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "clivoy.json")
			body := `{"providers":{"local":{"type":"codex","command":` + command + `}}}`
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "command") {
				t.Fatalf("invalid command accepted or unclear error: %v", err)
			}
		})
	}
}

func TestValidateCLICommandsCreatedInGo(t *testing.T) {
	for _, command := range []Command{{""}, {" \t"}, {"codex\x00"}, {"codex", "\x00"}} {
		c := Default()
		c.Providers["codex"] = Provider{Type: "codex", Command: command}
		if err := c.Validate(); err == nil {
			t.Fatalf("invalid command accepted: %q", command)
		}
	}
}
