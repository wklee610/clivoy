// Clivoy exposes local AI CLIs and OpenAI-compatible servers through one API.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"time"

	"github.com/wklee610/clivoy/internal/clidiscovery"
	"github.com/wklee610/clivoy/internal/config"
	"github.com/wklee610/clivoy/internal/engine"
	"github.com/wklee610/clivoy/internal/logfile"
	"github.com/wklee610/clivoy/internal/provider"
	"github.com/wklee610/clivoy/internal/server"
	"github.com/wklee610/clivoy/internal/state"
)

var version = "dev"
var commit = "unknown"
var date = "unknown"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "clivoy:", err)
		os.Exit(1)
	}
}

func run(args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		usage(out)
		return nil
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(out, "clivoy %s (commit %s, built %s)\n", version, commit, date)
		return nil
	case "help", "--help", "-h":
		usage(out)
		return nil
	case "keygen":
		if len(args) != 1 {
			return fmt.Errorf("usage: clivoy keygen")
		}
		key, err := generateAPIKey()
		if err != nil {
			return err
		}
		fmt.Fprintln(out, key)
		return nil
	case "init":
		return initConfig(args[1:], out, errOut)
	case "auth":
		return auth(args[1:], out, errOut)
	case "models":
		return listModels(args[1:], out, errOut)
	case "start", "stop", "status", "restart":
		return daemonCommand(args[0], args[1:], out, errOut)
	case "__daemon":
		return daemonChild(args[1:])
	case "serve", "doctor":
		return configured(args[0], args[1:], out, errOut)
	default:
		return fmt.Errorf("unknown command %q; run clivoy --help", args[0])
	}
}

func usage(w io.Writer) {
	_, _ = io.WriteString(w, `Clivoy — your AI CLIs and model servers, one API.

Usage:
  clivoy start [--config path]               Start the API server in the background
  clivoy stop                                Stop the background server
  clivoy status                              Show background server status
  clivoy restart [--config path]             Restart the server and reload configuration
  clivoy serve [--config path]               Run the API server in the foreground
  clivoy init [--output path]                Create configuration and an initial API key
  clivoy doctor [--config path]              Check configuration, API key setup and CLI paths
  clivoy models [--config path] [--refresh]  List model names to use in API requests
  clivoy auth [--config path]                Print the first configured API key
  clivoy keygen                              Generate a new API key without saving it
  clivoy version                             Show version and build information
  clivoy help                                Show this help (-h, --help)
`)
}

func generateAPIKey() (string, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate API key: %w", err)
	}
	return "sk-clivoy-" + hex.EncodeToString(random[:]), nil
}

func initConfig(args []string, out, errOut io.Writer) error {
	f := flag.NewFlagSet("init", flag.ContinueOnError)
	f.SetOutput(errOut)
	path := f.String("output", "", "configuration file to create (default: ~/.clivoy/clivoy.json)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() > 0 {
		return fmt.Errorf("unexpected arguments")
	}
	useDefault := *path == ""
	if useDefault {
		paths, err := state.Resolve()
		if err != nil {
			return err
		}
		if err := state.Ensure(paths); err != nil {
			return err
		}
		*path = paths.Config
	}
	key, err := generateAPIKey()
	if err != nil {
		return err
	}
	keyPath := filepath.Join(filepath.Dir(*path), "auth.json")
	c := config.Default()
	c.AuthFile = filepath.Base(keyPath)

	file, err := os.OpenFile(*path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	complete := false
	defer func() {
		_ = file.Close()
		if !complete {
			removeCreatedFile(*path, info)
		}
	}()
	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create auth file: %w", err)
	}
	keyInfo, err := keyFile.Stat()
	if err != nil {
		_ = keyFile.Close()
		return err
	}
	defer func() {
		_ = keyFile.Close()
		if !complete {
			removeCreatedFile(keyPath, keyInfo)
		}
	}()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if err := keyFile.Chmod(0600); err != nil {
		return err
	}
	authEncoder := json.NewEncoder(keyFile)
	authEncoder.SetIndent("", "  ")
	if err := authEncoder.Encode(struct {
		Keys []string `json:"keys"`
	}{Keys: []string{key}}); err != nil {
		return fmt.Errorf("write auth file: %w", err)
	}
	if err := keyFile.Close(); err != nil {
		return err
	}
	setDiscoveredCLICommands(&c, clidiscovery.Resolve)
	e := json.NewEncoder(file)
	e.SetIndent("", "  ")
	writeErr := e.Encode(c)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	complete = true
	fmt.Fprintf(out, "Created %s and auth file %s.\n", *path, keyPath)
	reportMissingCLIs(c, *path, out, clidiscovery.Resolve)
	if useDefault {
		fmt.Fprintln(out, "Run clivoy start.\nTo retrieve the client token, run clivoy auth.")
	} else {
		fmt.Fprintf(out, "Run clivoy start --config %q.\nTo retrieve the client token, run clivoy auth --config %q.\n", *path, *path)
	}
	return nil
}

// Save the CLI entrypoint, not its interpreter, so npm wrappers still resolve
// their paired Node executable when the configuration is loaded later.
func setDiscoveredCLICommands(c *config.Config, resolve func(string, string) (clidiscovery.Resolution, error)) {
	for _, name := range providerNames(*c) {
		p := c.Providers[name]
		if !p.IsCLI() || len(p.Command) != 0 {
			continue
		}
		resolved, err := resolve(p.Type, "")
		if err != nil {
			continue
		}
		p.Command = config.Command{resolved.Command}
		c.Providers[name] = p
	}
}

func reportMissingCLIs(c config.Config, path string, out io.Writer, resolve func(string, string) (clidiscovery.Resolution, error)) {
	for _, name := range providerNames(c) {
		p := c.Providers[name]
		if !p.IsCLI() {
			continue
		}
		if _, err := resolve(p.Type, p.Command.Executable()); err != nil {
			fmt.Fprintf(out, "%s CLI could not be resolved. Set providers.%s.command in %q to an array beginning with the absolute path of the %s executable, then run clivoy doctor.\n", p.Type, name, path, p.Type)
		}
	}
}

// Roll back only a file this invocation created. A pre-existing file or a
// replacement installed at the same path must never be deleted.
func removeCreatedFile(path string, created os.FileInfo) {
	current, err := os.Lstat(path)
	if err == nil && current.Mode().IsRegular() && os.SameFile(created, current) {
		_ = os.Remove(path)
	}
}

func auth(args []string, out, errOut io.Writer) error {
	f := flag.NewFlagSet("auth", flag.ContinueOnError)
	f.SetOutput(errOut)
	path := f.String("config", "", "JSON configuration file (default: ~/.clivoy/clivoy.json)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() > 0 {
		return fmt.Errorf("unexpected arguments")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	key, err := c.ResolveAPIKey()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, key)
	return err
}

func configured(command string, args []string, out, errOut io.Writer) (resultErr error) {
	return configuredRuntime(command, args, out, errOut, nil)
}

func configuredRuntime(command string, args []string, out, errOut io.Writer, managed *daemonRuntime) (resultErr error) {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(errOut)
	path := f.String("config", "", "JSON configuration file (default: ~/.clivoy/clivoy.json)")
	listen := f.String("listen", "", "override the listen address")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() > 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if command == "serve" && *path == "" {
		paths, err := state.Resolve()
		if err != nil {
			return err
		}
		if _, err := os.Lstat(paths.Config); errors.Is(err, os.ErrNotExist) {
			if err := initConfig(nil, out, errOut); err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("inspect default configuration: %w", err)
		}
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	if *listen != "" {
		c.Listen = *listen
	}
	if command == "doctor" {
		configPath := *path
		if configPath == "" {
			paths, err := state.Resolve()
			if err != nil {
				return err
			}
			configPath = paths.Config
		}
		configPath, err = filepath.Abs(configPath)
		if err != nil {
			return fmt.Errorf("resolve config path: %w", err)
		}
		return doctor(c, configPath, out)
	}
	keys, err := c.ResolveAPIKeys()
	if err != nil {
		return err
	}
	logWriter, err := runtimeStorage(c)
	if err != nil {
		return err
	}
	if logWriter != nil {
		defer func() {
			if resultErr != nil {
				slog.New(slog.NewTextHandler(logWriter, nil)).Error("server failed", "error", resultErr)
			}
			resultErr = errors.Join(resultErr, logWriter.Close())
		}()
		out = io.MultiWriter(out, logWriter)
		errOut = io.MultiWriter(errOut, logWriter)
	}
	providers := make(map[string]engine.Provider, len(c.Providers))
	for name, p := range c.Providers {
		var backend engine.Provider
		switch p.Type {
		case "codex", "claude", "gemini", "grok":
			backend, err = provider.NewCLI(p.Type, p.Command, p.WorkDir, excludedCLIEnvironment(c, p.Type)...)
		case "openai", "ollama":
			upstreamKey := ""
			if p.APIKeyEnv != "" {
				upstreamKey = os.Getenv(p.APIKeyEnv)
				if upstreamKey == "" {
					return fmt.Errorf("provider %q: environment variable %s is empty", name, p.APIKeyEnv)
				}
			}
			if p.Type == "ollama" {
				backend, err = provider.NewOllama(p.BaseURL, upstreamKey)
			} else {
				backend, err = provider.NewOpenAI(p.BaseURL, upstreamKey)
			}
		}
		if err != nil {
			return fmt.Errorf("provider %q: %w", name, err)
		}
		providers[name] = backend
	}
	knownModels, warnings := modelChoices(c, false)
	for _, warning := range warnings {
		fmt.Fprintln(errOut, warning)
	}
	models := make(map[string]server.Model, len(knownModels))
	for alias, m := range knownModels {
		models[alias] = server.Model{Provider: providers[m.Provider], UpstreamModel: m.Model, Owner: m.Provider, Capabilities: m.Capabilities}
	}
	nativeProviders := make(map[string]server.Model)
	for name, p := range c.Providers {
		if p.SupportsNativeModels() {
			caps := []string{"chat"}
			if p.IsCLI() {
				caps = p.CLICapabilities()
			}
			nativeProviders[name] = server.Model{Provider: providers[name], Owner: name, Capabilities: caps}
		}
	}
	timeout, _ := time.ParseDuration(c.RequestTimeout)
	handler, err := server.New(server.Options{APIKeys: keys, RequestTimeout: timeout, MaxConcurrent: c.MaxConcurrent, MaxQueue: c.MaxQueue, MaxBodyBytes: c.MaxBodyBytes, Models: models, NativeProviders: nativeProviders})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer stop()
	if managed != nil {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		logPath, err := c.ResolveLogFile()
		if err != nil {
			return err
		}
		managed.record.LogFile = logPath
		if err := managed.control(cancel); err != nil {
			return err
		}
		defer managed.close()
		return serveReady(ctx, c.Listen, handler, out, errOut, managed.ready)
	}
	return serve(ctx, c.Listen, handler, out, errOut)
}

// Prepare only the log storage needed by this configuration. Explicit configs
// with log_file: "-" do not need a writable home directory.
func runtimeStorage(c config.Config) (io.WriteCloser, error) {
	if c.LogFile == "" {
		paths, err := state.Resolve()
		if err != nil {
			return nil, err
		}
		if err := state.Ensure(paths); err != nil {
			return nil, err
		}
	}
	logPath, err := c.ResolveLogFile()
	if err != nil {
		return nil, err
	}
	if logPath == "-" {
		return nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	writer, err := logfile.Open(logPath)
	if err != nil {
		return nil, fmt.Errorf("open server log: %w", err)
	}
	return writer, nil
}

func doctor(c config.Config, configPath string, out io.Writer) error {
	names := make([]string, 0, len(c.Providers))
	for n := range c.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(out, "Clivoy %s\nConfig: %s\nListen: %s\n", version, configPath, c.Listen)
	if _, err := c.ResolveAPIKey(); err != nil {
		fmt.Fprintln(out, "API key: missing or invalid (check api_key_env, auth_file, and legacy api_key_file)")
	} else {
		fmt.Fprintln(out, "API key: configured")
	}
	for _, name := range names {
		p := c.Providers[name]
		if p.Type == "ollama" {
			fmt.Fprintf(out, "%s: Ollama HTTP backend (run clivoy models to check installed models)\n", name)
			continue
		}
		if p.Type == "openai" {
			fmt.Fprintf(out, "%s: OpenAI-compatible HTTP backend (connectivity not checked)\n", name)
			continue
		}
		resolved, err := clidiscovery.Resolve(p.Type, p.Command.Executable())
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", name, err)
			fmt.Fprintf(out, "  Set providers.%s.command in the config above to an array beginning with the executable's absolute path.\n", name)
		} else {
			source := "auto-detected"
			if len(p.Command) != 0 {
				source = "configured"
			}
			fmt.Fprintf(out, "%s: %s (%s; login not checked)\n", name, resolved.Command, source)
		}
	}
	return nil
}

func serve(ctx context.Context, addr string, handler http.Handler, out, errOut io.Writer) error {
	return serveReady(ctx, addr, handler, out, errOut, nil)
}

func serveReady(ctx context.Context, addr string, handler http.Handler, out, errOut io.Writer, ready func(net.Addr) error) error {
	logger := slog.New(slog.NewTextHandler(errOut, nil))
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	srv.ErrorLog = slog.NewLogLogger(logger.Handler(), slog.LevelError)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	if ready != nil {
		if err := ready(listener.Addr()); err != nil {
			return err
		}
	}
	srv.BaseContext = func(net.Listener) context.Context { return ctx }
	done := make(chan struct{})
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := srv.Shutdown(shutdown); err != nil {
				_ = srv.Close()
			}
		case <-done:
		}
	}()
	fmt.Fprintf(out, "Clivoy listening on http://%s\n", listener.Addr())
	logger.Info("server started", "version", version)
	err = srv.Serve(listener)
	if !errors.Is(err, http.ErrServerClosed) {
		_ = srv.Close()
	}
	close(done)
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		logger.Info("server stopped")
		return nil
	}
	return err
}
