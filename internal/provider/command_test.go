package provider

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCLIConfiguredArgumentsReachChild(t *testing.T) {
	for _, tc := range []struct {
		kind, flag, capture string
	}{
		{"codex", "--yolo", "capture"},
		{"codex", "--dangerously-bypass-approvals-and-sandbox", "capture"},
		{"claude", "--dangerously-skip-permissions", "capture_claude"},
		{"gemini", "--debug", "capture_gemini"},
	} {
		t.Run(tc.kind+tc.flag, func(t *testing.T) {
			t.Setenv("CLIVOY_TEST_PROVIDER_CHILD", tc.capture)
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			literal := `space and $(never-execute); "quotes"`
			command := []string{binary, tc.flag, "--example", literal, ""}
			backend, err := NewCLI(tc.kind, command, "")
			if err != nil {
				t.Fatal(err)
			}
			// Configuration is owned by the provider after construction.
			command[1] = "--mutated-after-construction"
			result, err := backend.Generate(context.Background(), basicRequest(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var capture struct {
				Args  []string `json:"args"`
				Stdin string   `json:"stdin"`
			}
			if err := json.Unmarshal([]byte(result.Text), &capture); err != nil {
				t.Fatal(err)
			}
			want := []string{tc.flag, "--example", literal, ""}
			offset := 0
			if tc.kind == "codex" {
				want = append([]string{"exec"}, want...)
				offset = 1
			}
			if len(capture.Args) < len(want) || !reflect.DeepEqual(capture.Args[:len(want)], want) {
				t.Fatalf("command arguments changed: %q", capture.Args)
			}
			if forbidsTools := strings.Contains(capture.Stdin, "Do not use tools or inspect the local environment."); forbidsTools != (tc.kind == "gemini") {
				t.Fatalf("prompt disagrees with configured execution mode: %s", capture.Stdin)
			}
			if tc.kind == "codex" {
				for _, arg := range capture.Args {
					if arg == "--sandbox" || strings.HasPrefix(arg, "approval_policy=") {
						t.Fatalf("bypass conflicts with default argument %q", arg)
					}
				}
				if capture.Args[len(capture.Args)-1] != "-" || !slices.Contains(capture.Args, "--json") {
					t.Fatalf("Codex protocol flags missing: %q", capture.Args)
				}
			} else if tc.kind == "claude" {
				if slices.Contains(capture.Args, "--tools") || slices.Contains(capture.Args, "--permission-mode") {
					t.Fatalf("Claude bypass still disables tools: %q", capture.Args)
				}
				if !slices.Contains(capture.Args, "--print") || !slices.Contains(capture.Args, "stream-json") {
					t.Fatalf("Claude protocol flags missing: %q", capture.Args)
				}
			}
			// Building adapter arguments must leave the configuration intact.
			p := backend.(*cliProvider)
			if !reflect.DeepEqual(p.commandArgs, want[offset:]) {
				t.Fatal("request argument construction mutated configured arguments")
			}
		})
	}
}

func TestCLICommandValidationAndDefaultDiscovery(t *testing.T) {
	for _, command := range [][]string{{""}, {" "}, {"codex\x00"}, {"codex", "--bad\x00"}} {
		if _, err := NewCLI("codex", command, ""); err == nil {
			t.Errorf("accepted invalid command %q", command)
		}
	}
	for _, command := range [][]string{nil, {}} {
		backend, err := NewCLI("codex", command, "")
		if err != nil || backend.(*cliProvider).binary != "" {
			t.Fatalf("automatic discovery rejected: %v", err)
		}
	}
}

func TestCLIArgumentsWithoutBypassKeepDefaults(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		backend, err := NewCLI(kind, []string{kind, "--example", "some value"}, "")
		if err != nil {
			t.Fatal(err)
		}
		args := backend.(*cliProvider).args(basicRequest())
		_, input, cleanup, err := backend.(*cliProvider).prepareInput(basicRequest())
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		if !strings.Contains(input, "Do not use tools or inspect the local environment.") {
			t.Fatalf("default tool restriction missing from prompt: %s", input)
		}
		if kind == "codex" && (!slices.Contains(args, "read-only") || !slices.Contains(args, `approval_policy="never"`)) {
			t.Fatalf("Codex default restrictions lost: %q", args)
		}
		if kind == "claude" && (!slices.Contains(args, "--tools") || !slices.Contains(args, "dontAsk")) {
			t.Fatalf("Claude default restrictions lost: %q", args)
		}
	}
	p := &cliProvider{kind: "grok", commandArgs: []string{"--example", "some value"}}
	if args := p.args(basicRequest()); !reflect.DeepEqual(args[:2], p.commandArgs) {
		t.Fatalf("Grok command arguments missing: %q", args)
	}
}
