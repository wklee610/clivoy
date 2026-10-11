// Package server exposes OpenAI and Anthropic wire protocols over one provider contract.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/wklee610/clivoy/internal/engine"
)

type Model struct {
	Provider      engine.Provider
	UpstreamModel string
	Owner         string
	Capabilities  []string
}

type Options struct {
	APIKey         string
	APIKeys        []string
	RequestTimeout time.Duration
	MaxConcurrent  int
	MaxQueue       int
	MaxBodyBytes   int64
	Models         map[string]Model
	// NativeProviders permits <provider>/<native-model> selection for explicitly
	// registered CLI and Ollama backends. Generic HTTP backends remain alias-only.
	NativeProviders map[string]Model
}

type Server struct {
	options      Options
	apiKeyHashes [][sha256.Size]byte
	slots        chan struct{}
	queue        chan struct{}
}

func New(o Options) (*Server, error) {
	keys := o.APIKeys
	if o.APIKey != "" {
		keys = append([]string{o.APIKey}, keys...)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("API key is required")
	}
	keyHashes := make([][sha256.Size]byte, len(keys))
	for i, key := range keys {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("API keys must not be empty or whitespace")
		}
		keyHashes[i] = sha256.Sum256([]byte(key))
	}
	// Keep independent fixed-length hashes instead of retaining caller-owned keys.
	o.APIKey, o.APIKeys = "", nil
	if o.RequestTimeout <= 0 || o.MaxConcurrent < 1 || o.MaxQueue < 0 || o.MaxBodyBytes < 1 {
		return nil, fmt.Errorf("invalid server limits")
	}
	if len(o.Models) == 0 && len(o.NativeProviders) == 0 {
		return nil, fmt.Errorf("at least one model or native model provider is required")
	}
	models, err := cloneModels(o.Models)
	if err != nil {
		return nil, err
	}
	nativeProviders, err := cloneModels(o.NativeProviders)
	if err != nil {
		return nil, fmt.Errorf("native model providers: %w", err)
	}
	o.Models, o.NativeProviders = models, nativeProviders
	return &Server{options: o, apiKeyHashes: keyHashes, slots: make(chan struct{}, o.MaxConcurrent), queue: make(chan struct{}, o.MaxQueue)}, nil
}

func cloneModels(source map[string]Model) (map[string]Model, error) {
	models := make(map[string]Model, len(source))
	for k, v := range source {
		if v.Provider == nil {
			return nil, fmt.Errorf("model %q has no provider", k)
		}
		if v.Capabilities == nil {
			v.Capabilities = []string{"chat"}
		} else {
			v.Capabilities = append([]string{}, v.Capabilities...)
		}
		seen := map[string]bool{}
		for _, c := range v.Capabilities {
			if c != "chat" && c != "vision" && c != "image_generation" {
				return nil, fmt.Errorf("model %q has unknown capability %q", k, c)
			}
			if seen[c] {
				return nil, fmt.Errorf("model %q has duplicate capability %q", k, c)
			}
			seen[c] = true
		}
		if len(seen) == 0 || (seen["vision"] && !seen["chat"]) {
			return nil, fmt.Errorf("model %q has invalid capabilities", k)
		}
		if seen["image_generation"] {
			if _, ok := v.Provider.(engine.ImageProvider); !ok {
				return nil, fmt.Errorf("model %q requires an image generation provider", k)
			}
		}
		models[k] = v
	}
	return models, nil
}

func (s *Server) resolveChatModel(name string) (Model, error) {
	if m, ok := s.options.Models[name]; ok {
		return m, nil
	}
	var m Model
	prefixLength := 0
	for provider, candidate := range s.options.NativeProviders {
		prefix := provider + "/"
		if len(prefix) > prefixLength && strings.HasPrefix(name, prefix) {
			m, prefixLength = candidate, len(prefix)
		}
	}
	if prefixLength == 0 {
		return Model{}, &engine.Error{Status: 404, Type: "not_found_error", Message: "Unknown model; use GET /v1/models to list configured models"}
	}
	native := name[prefixLength:]
	if !validNativeModel(native) {
		return Model{}, engine.Invalid("Native model ID must be 1-256 ASCII characters, start with a letter or digit, and contain only letters, digits, _-.:/[]")
	}
	m.UpstreamModel = native
	return m, nil
}

func validNativeModel(name string) bool {
	if len(name) == 0 || len(name) > 256 || !asciiAlphanumeric(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !asciiAlphanumeric(name[i]) && !strings.ContainsRune("_-.:/[]", rune(name[i])) {
			return false
		}
	}
	return true
}

func asciiAlphanumeric(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Request-ID", newID("req"))
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	protocol := "openai"
	if strings.HasPrefix(r.URL.Path, "/v1/messages") {
		protocol = "anthropic"
	}
	if !s.authorized(r, protocol) {
		s.writeError(w, protocol, &engine.Error{Status: 401, Type: "authentication_error", Message: "Invalid or missing Clivoy API key"})
		return
	}
	switch r.URL.Path {
	case "/v1/models":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			s.writeError(w, protocol, &engine.Error{Status: 405, Type: "invalid_request_error", Message: "Method not allowed"})
			return
		}
		names := make([]string, 0, len(s.options.Models))
		for n := range s.options.Models {
			names = append(names, n)
		}
		sort.Strings(names)
		data := make([]any, 0, len(names))
		for _, n := range names {
			data = append(data, map[string]any{"id": n, "object": "model", "created": 0, "owned_by": s.options.Models[n].Owner, "capabilities": s.options.Models[n].Capabilities})
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": data})
		return
	case "/v1/chat/completions", "/v1/messages", "/v1/responses", "/v1/images/generations":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			s.writeError(w, protocol, &engine.Error{Status: 405, Type: "invalid_request_error", Message: "Method not allowed"})
			return
		}
	default:
		s.writeError(w, protocol, &engine.Error{Status: 404, Type: "not_found_error", Message: "Endpoint not found"})
		return
	}
	if media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || media != "application/json" {
		s.writeError(w, protocol, &engine.Error{Status: 415, Type: "invalid_request_error", Message: "Content-Type must be application/json"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.options.MaxBodyBytes)
	if r.URL.Path == "/v1/images/generations" {
		s.generateImage(w, r)
		return
	}
	p, err := parseRequest(r)
	if err != nil {
		s.writeError(w, protocol, err)
		return
	}
	normalizeRequest(&p.Request)
	if err = validateRequest(p.Request); err != nil {
		s.writeError(w, protocol, err)
		return
	}
	m, err := s.resolveChatModel(p.Request.Model)
	if err != nil {
		s.writeError(w, protocol, err)
		return
	}
	if !m.supports("chat") {
		s.writeError(w, protocol, engine.Invalid("Selected model does not support chat; choose a chat model from /v1/models"))
		return
	}
	if hasImageInput(p.Request) && !m.supports("vision") {
		s.writeError(w, protocol, engine.Invalid("Selected model does not support image input; choose a model with vision capability from /v1/models"))
		return
	}
	native := p.Request
	native.Model = m.UpstreamModel
	if err = m.Provider.Validate(native); err != nil {
		s.writeError(w, protocol, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.options.RequestTimeout)
	defer cancel()
	if err = s.acquire(ctx); err != nil {
		s.writeError(w, protocol, err)
		return
	}
	defer func() { <-s.slots }()
	if p.Stream {
		s.stream(ctx, w, p, m.Provider, native)
		return
	}
	result, err := m.Provider.Generate(ctx, native, nil)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.writeError(w, protocol, err)
		return
	}
	if err = validateResult(result); err != nil {
		s.writeError(w, protocol, err)
		return
	}
	writeJSON(w, 200, p.response(result, newID(p.idPrefix()), "completed"))
}

func (s *Server) authorized(r *http.Request, protocol string) bool {
	key := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		key = strings.TrimPrefix(h, "Bearer ")
	}
	if key == "" && protocol == "anthropic" {
		key = r.Header.Get("X-Api-Key")
	}
	digest := sha256.Sum256([]byte(key))
	match := 0
	for _, expected := range s.apiKeyHashes {
		match |= subtle.ConstantTimeCompare(digest[:], expected[:])
	}
	return match == 1
}

func (s *Server) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.slots
			return err
		}
		return nil
	default:
	}
	select {
	case s.queue <- struct{}{}:
	default:
		return &engine.Error{Status: 429, Type: "rate_limit_error", Message: "Clivoy queue is full; retry later"}
	}
	defer func() { <-s.queue }()
	select {
	case s.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.slots
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func clientError(err error) *engine.Error {
	var e *engine.Error
	if errors.As(err, &e) {
		return e
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &engine.Error{Status: 413, Type: "invalid_request_error", Message: "Request body is too large"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &engine.Error{Status: 504, Type: "timeout_error", Message: "Request timed out"}
	}
	if errors.Is(err, context.Canceled) {
		return &engine.Error{Status: 408, Type: "request_cancelled", Message: "Request was cancelled"}
	}
	return &engine.Error{Status: 502, Type: "api_error", Message: "Provider request failed"}
}

func (s *Server) writeError(w http.ResponseWriter, protocol string, err error) {
	e := clientError(err)
	if e.Status == 429 {
		w.Header().Set("Retry-After", "1")
	}
	if protocol == "anthropic" {
		writeJSON(w, e.Status, map[string]any{"type": "error", "error": map[string]any{"type": anthropicErrorType(e), "message": e.Message}})
		return
	}
	writeJSON(w, e.Status, map[string]any{"error": map[string]any{"message": e.Message, "type": e.Type, "param": nil, "code": e.Type}})
}

func anthropicErrorType(e *engine.Error) string {
	switch e.Status {
	case 400, 413, 415, 405:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 404:
		return "not_found_error"
	case 429:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func newID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
func rawString(s string) json.RawMessage { b, _ := json.Marshal(s); return b }

func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return err
		}
		return engine.Invalid("Invalid JSON request: " + err.Error())
	}
	if err := d.Decode(new(any)); err != io.EOF {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			return err
		}
		return engine.Invalid("Request must contain a single JSON object")
	}
	return nil
}

func validateResult(r engine.Result) error {
	for _, t := range r.ToolCalls {
		var object map[string]any
		if t.ID == "" || t.Function.Name == "" || json.Unmarshal([]byte(t.Function.Arguments), &object) != nil || object == nil {
			return &engine.Error{Status: 502, Type: "api_error", Message: "Provider returned an invalid function call"}
		}
	}
	return nil
}
