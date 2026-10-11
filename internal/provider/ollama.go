package provider

import (
	"errors"

	"github.com/wklee610/clivoy/internal/engine"
)

// ollamaProvider exposes the Chat Completions adapter without advertising the
// optional Images API implemented by the general OpenAI-compatible provider.
type ollamaProvider struct{ engine.Provider }

// NewOllama connects to Ollama's OpenAI-compatible API prefix. An empty base URL
// selects the local server; local Ollama does not require an API key.
func NewOllama(baseURL, apiKey string) (engine.Provider, error) {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434/v1"
	}
	p, err := NewOpenAI(baseURL, apiKey)
	if err != nil {
		return nil, errors.New("Ollama base URL must be an absolute http(s) API prefix without credentials, query, or fragment")
	}
	return &ollamaProvider{Provider: p}, nil
}
