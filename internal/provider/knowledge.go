package provider

import "github.com/wklee610/clivoy/internal/engine"

// NewKnowledgeCLI reuses only the configured executable. Generation arguments
// and work_dir from the ordinary provider are deliberately not inherited by a
// document-answering model. Each request gets a fresh temporary directory.
func NewKnowledgeCLI(kind string, command []string, excludedEnv ...string) (engine.Provider, error) {
	if len(command) > 0 {
		command = command[:1]
	}
	backend, err := NewCLI(kind, command, "", excludedEnv...)
	if err != nil {
		return nil, err
	}
	p := backend.(*cliProvider)
	if kind == "codex" {
		// Explicit flags also cover CLI builds whose built-in defaults enable
		// integrations without a user config. Unknown feature flags fail closed
		// at the CLI rather than retrying a less restricted invocation.
		for _, feature := range []string{
			"shell_tool", "unified_exec", "apps", "plugins", "multi_agent",
			"multi_agent_v2", "browser_use", "browser_use_external", "computer_use",
			"in_app_browser", "image_generation", "view_image", "hooks", "memories",
			"goals", "remote_plugin", "skill_search", "workspace_dependencies",
			"code_mode", "code_mode_host",
		} {
			p.commandArgs = append(p.commandArgs, "--disable", feature)
		}
		p.commandArgs = append(p.commandArgs,
			"-c", "skills.bundled.enabled=false",
			"-c", "skills.include_instructions=false",
			"-c", "project_doc_max_bytes=0",
			"-c", "mcp_servers={}",
		)
	} else if kind == "claude" {
		p.commandArgs = []string{"--setting-sources", ""}
	}
	return p, nil
}
