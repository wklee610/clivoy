package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxOllamaModels        = 1000
	ollamaDiscoveryTimeout = 5 * time.Second
)

// DiscoverOllama lists models exposed by an Ollama server without starting
// inference or downloading models. baseURL is the OpenAI-compatible API prefix,
// such as http://127.0.0.1:11434/v1; an empty value selects that local default.
// The list endpoint does not identify vision support, so entries advertise chat
// only. Administrators can opt into vision using an explicit configured alias.
func DiscoverOllama(ctx context.Context, baseURL, apiKey string) ([]Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434/v1"
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("Ollama base URL must be an absolute http(s) API prefix without credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	u.RawPath = ""
	ctx, cancel := context.WithTimeout(ctx, ollamaDiscoveryTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("could not prepare Ollama model discovery")
	}
	request.Header.Set("Accept", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{
		// Never forward a configured key to a redirect destination.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("could not connect to Ollama for model discovery")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Ollama model discovery returned HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxCatalogBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("could not read Ollama model metadata")
	}
	if len(body) > maxCatalogBytes {
		return nil, errors.New("Ollama model metadata exceeded the 4 MiB size limit")
	}
	var catalog struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &catalog) != nil || catalog.Data == nil {
		return nil, errors.New("Ollama returned invalid model metadata")
	}
	if len(catalog.Data) > maxOllamaModels {
		return nil, errors.New("Ollama model metadata exceeded the model count limit")
	}
	models := make([]Model, 0, len(catalog.Data))
	for _, entry := range catalog.Data {
		models = append(models, Model{ID: entry.ID, Name: entry.ID, Capabilities: []string{"chat"}})
	}
	return normalize(models), nil
}
