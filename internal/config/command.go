package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Command contains a CLI executable followed by separate, literal arguments.
// An empty command requests automatic executable discovery.
type Command []string

// Executable returns the configured executable, or an empty string for discovery.
func (c Command) Executable() string {
	if len(c) == 0 {
		return ""
	}
	return c[0]
}

// UnmarshalJSON also accepts the executable-only string used before v1.0.1.
// Strings are never split or interpreted as shell commands.
func (c *Command) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	var decoded Command
	if bytes.Equal(data, []byte("null")) {
		*c = nil
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var executable string
		if err := json.Unmarshal(data, &executable); err != nil {
			return err
		}
		if executable != "" {
			decoded = Command{executable}
		}
	} else {
		var elements []json.RawMessage
		if err := json.Unmarshal(data, &elements); err != nil {
			return fmt.Errorf("command must be an array of strings or a legacy executable string: %w", err)
		}
		decoded = make(Command, len(elements))
		for i, element := range elements {
			if len(element) == 0 || element[0] != '"' {
				return fmt.Errorf("command[%d] must be a string", i)
			}
			if err := json.Unmarshal(element, &decoded[i]); err != nil {
				return fmt.Errorf("command[%d] must be a string: %w", i, err)
			}
		}
	}
	if err := decoded.validate(); err != nil {
		return err
	}
	*c = decoded
	return nil
}

func (c Command) validate() error {
	if len(c) > 0 && strings.TrimSpace(c[0]) == "" {
		return fmt.Errorf("command executable must not be empty or whitespace-only")
	}
	for i, value := range c {
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("command[%d] must not contain NUL", i)
		}
	}
	return nil
}
