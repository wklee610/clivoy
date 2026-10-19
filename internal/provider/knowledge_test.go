package provider

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestKnowledgeCLIUsesRestrictedInvocation(t *testing.T) {
	for _, tc := range []struct{ kind, flag, capture string }{
		{"codex", "--yolo", "capture"},
		{"claude", "--dangerously-skip-permissions", "capture_claude"},
		{"gemini", "--yolo", "capture_gemini"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			t.Setenv("CLIVOY_TEST_PROVIDER_CHILD", tc.capture)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			command := []string{executable, tc.flag, "--arbitrary-option", "must-not-be-forwarded"}
			backend, err := NewKnowledgeCLI(tc.kind, command)
			if err != nil {
				t.Fatal(err)
			}
			result, err := backend.Generate(context.Background(), basicRequest(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var capture struct {
				Args       []string
				Stdin, CWD string
			}
			if err := json.Unmarshal([]byte(result.Text), &capture); err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range command[1:] {
				if slices.Contains(capture.Args, forbidden) {
					t.Fatalf("knowledge invocation inherited %s", forbidden)
				}
			}
			if !strings.Contains(capture.Stdin, "Do not use tools or inspect the local environment") {
				t.Fatal("knowledge prompt lost tool restriction")
			}
			if _, err := os.Stat(capture.CWD); !os.IsNotExist(err) {
				t.Fatalf("request directory was not removed: %v", err)
			}
			args := strings.Join(capture.Args, " ")
			if tc.kind == "codex" {
				for _, want := range []string{"--sandbox read-only", "--disable shell_tool", "--disable unified_exec", "--disable apps", "--disable plugins", "--disable code_mode_host", "project_doc_max_bytes=0", "mcp_servers={}"} {
					if !strings.Contains(args, want) {
						t.Errorf("missing %s in %s", want, args)
					}
				}
			}
			if tc.kind == "claude" && (!slices.Contains(capture.Args, "--tools") || !slices.Contains(capture.Args, "dontAsk") || !slices.Contains(capture.Args, "--setting-sources")) {
				t.Fatalf("Claude knowledge controls missing: %q", capture.Args)
			}
			if command[1] != tc.flag {
				t.Fatal("normal provider command changed")
			}
		})
	}
}
