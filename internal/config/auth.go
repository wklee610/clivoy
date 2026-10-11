package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// ResolveAPIKey returns the first configured key for single-key CLI consumers.
func (c Config) ResolveAPIKey() (string, error) {
	keys, err := c.ResolveAPIKeys()
	if err != nil {
		return "", err
	}
	return keys[0], nil
}

// ResolveAPIKeys uses a nonempty environment override, then the JSON auth file,
// then the legacy plain-text key file. A configured auth file must be valid;
// errors never fall back to the legacy file. No key material is logged here.
func (c Config) ResolveAPIKeys() ([]string, error) {
	if key := strings.TrimSpace(os.Getenv(c.APIKeyEnv)); key != "" {
		return []string{key}, nil
	}
	if c.AuthFile != "" {
		data, err := c.readPrivateAuthFile(c.AuthFile, "auth file", 64<<10)
		if err != nil {
			return nil, err
		}
		return decodeAuthKeys(data)
	}
	if strings.TrimSpace(c.APIKeyFile) == "" {
		return nil, fmt.Errorf("set %s to a nonempty server API key or run clivoy init", c.APIKeyEnv)
	}
	data, err := c.readPrivateAuthFile(c.APIKeyFile, "API key file", 4096)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return nil, fmt.Errorf("API key file is empty")
	}
	return []string{key}, nil
}

func (c Config) readPrivateAuthFile(path, label string, maxBytes int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.configDir, path)
	}
	before, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s is missing (run clivoy init): %w", label, err)
		}
		return nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a symlink or special file", label)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened %s: %w", label, err)
	}
	after, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || !after.Mode().IsRegular() || !os.SameFile(info, after) {
		return nil, fmt.Errorf("%s changed while opening it", label)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("%s permissions must not allow group or other access; use chmod 600", label)
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return data, nil
}

func decodeAuthKeys(data []byte) ([]string, error) {
	// Inspect the object tokens so duplicate or differently cased fields cannot
	// silently replace the administrator's key list. Never expose parser errors:
	// an unknown field or malformed token could itself contain key material.
	invalid := fmt.Errorf("auth file must contain one JSON object with a keys array")
	d := json.NewDecoder(bytes.NewReader(data))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return nil, invalid
	}
	var keys []string
	seen := false
	for d.More() {
		field, err := d.Token()
		if err != nil || field != "keys" || seen {
			return nil, invalid
		}
		seen = true
		if err := d.Decode(&keys); err != nil {
			return nil, invalid
		}
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return nil, invalid
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, invalid
	}
	if len(keys) == 0 || len(keys) > 128 {
		return nil, fmt.Errorf("auth file must contain between 1 and 128 keys")
	}
	for _, key := range keys {
		if key == "" || strings.IndexFunc(key, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return nil, fmt.Errorf("auth file keys must be nonempty and contain no whitespace or control characters")
		}
	}
	return keys, nil
}
