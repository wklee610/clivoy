package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wklee610/clivoy/internal/daemon"
	"github.com/wklee610/clivoy/internal/state"
)

const daemonStartupTimeout = 15 * time.Second

// The random control token belongs only to this instance, not to API clients.
// Never put API keys, provider credentials, or the process environment here.
type daemonRecord struct {
	PID        int    `json:"pid"`
	APIAddress string `json:"api_address"`
	ControlURL string `json:"control_url"`
	Token      string `json:"token"`
	Config     string `json:"config"`
	Listen     string `json:"listen"`
	StartedAt  string `json:"started_at"`
	LogFile    string `json:"log_file"`
}

type daemonOptions struct {
	config, listen       string
	configSet, listenSet bool
}

func daemonCommand(command string, args []string, out, errOut io.Writer) error {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(errOut)
	opts := daemonOptions{}
	if command == "start" || command == "restart" {
		f.StringVar(&opts.config, "config", "", "JSON configuration file (default: ~/.clivoy/clivoy.json)")
		f.StringVar(&opts.listen, "listen", "", "override the listen address")
	}
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	f.Visit(func(v *flag.Flag) {
		opts.configSet = opts.configSet || v.Name == "config"
		opts.listenSet = opts.listenSet || v.Name == "listen"
	})
	paths, err := state.Resolve()
	if err != nil {
		return err
	}
	if command == "status" || command == "stop" {
		if _, err := os.Lstat(paths.Root); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(out, "Clivoy is stopped.")
			return nil
		}
	}
	if err := state.Ensure(paths); err != nil {
		return err
	}
	if command == "status" {
		record, running, err := inspectDaemon(paths)
		if err != nil {
			return err
		}
		if running {
			fmt.Fprintf(out, "Clivoy is running (PID %d) at http://%s\n", record.PID, record.APIAddress)
			fmt.Fprintf(out, "Config: %s\nLog: %s\n", record.Config, record.LogFile)
		} else {
			fmt.Fprintln(out, "Clivoy is stopped.")
		}
		return nil
	}
	lock, err := commandLock(paths)
	if err != nil {
		return err
	}
	defer lock.Close()
	record, running, err := inspectDaemon(paths)
	if err != nil {
		return err
	}
	if command == "start" && running {
		fmt.Fprintf(out, "Clivoy is already running (PID %d) at http://%s; use clivoy restart to change settings.\n", record.PID, record.APIAddress)
		return nil
	}
	if command == "restart" && record != nil {
		if !opts.configSet {
			opts.config = record.Config
		}
		if !opts.listenSet {
			opts.listen = record.Listen
		}
	}
	// Resolve options before stopping a running instance, including relative paths.
	if opts.config != "" {
		opts.config, err = filepath.Abs(opts.config)
		if err != nil {
			return err
		}
	}
	if command == "stop" || command == "restart" {
		if running {
			if err := stopDaemon(paths, record); err != nil {
				return err
			}
			fmt.Fprintln(out, "Clivoy stopped.")
		} else {
			removeDaemonRecord(paths, record)
			if command == "stop" {
				fmt.Fprintln(out, "Clivoy is stopped.")
			}
		}
		if command == "stop" {
			return nil
		}
	}
	return startDaemon(paths, opts, out)
}

func commandLock(paths state.Paths) (*daemon.Lock, error) {
	deadline := time.Now().Add(35 * time.Second)
	for {
		lock, err := daemon.TryLock(filepath.Join(paths.Root, "daemon-command.lock"))
		if !errors.Is(err, daemon.ErrLocked) {
			return lock, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another Clivoy lifecycle command is still running; retry shortly")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func instanceLocked(paths state.Paths) (bool, error) {
	path := filepath.Join(paths.Root, "daemon.lock")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	lock, err := daemon.TryLock(path)
	if errors.Is(err, daemon.ErrLocked) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, lock.Close()
}

func inspectDaemon(paths state.Paths) (*daemonRecord, bool, error) {
	record, err := readDaemonRecord(paths)
	if err != nil {
		return nil, false, err
	}
	locked, err := instanceLocked(paths)
	if err != nil {
		return record, false, err
	}
	if !locked {
		return record, false, nil
	}
	if record == nil {
		return nil, false, fmt.Errorf("Clivoy is starting; retry clivoy status shortly")
	}
	if err := controlRequest(record, "/status"); err != nil {
		return record, false, fmt.Errorf("Clivoy holds its instance lock but is not responding; check %s: %w", record.LogFile, err)
	}
	return record, true, nil
}

func startDaemon(paths state.Paths, opts daemonOptions, out io.Writer) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	// Keep the short-lived startup handshake in the private state directory;
	// it does not need a persistent temporary-files subdirectory.
	handshake, err := os.CreateTemp(paths.Root, ".daemon-start-*.json")
	if err != nil {
		return err
	}
	readyPath := handshake.Name()
	if err := handshake.Close(); err != nil {
		_ = os.Remove(readyPath)
		return err
	}
	defer os.Remove(readyPath)
	args := []string{"__daemon", "--ready-file", readyPath}
	if opts.config != "" {
		args = append(args, "--config", opts.config)
	}
	if opts.listen != "" {
		args = append(args, "--listen", opts.listen)
	}
	pid, err := daemon.Launch(executable, args, os.Environ(), dir)
	if err != nil {
		return fmt.Errorf("start background process: %w", err)
	}
	deadline := time.Now().Add(daemonStartupTimeout)
	for time.Now().Before(deadline) {
		var result struct {
			Ready bool   `json:"ready"`
			Error string `json:"error"`
		}
		if err := readPrivateJSON(readyPath, &result); err == nil {
			if result.Error != "" {
				return fmt.Errorf("background startup failed: %s", result.Error)
			}
			if result.Ready {
				record, running, err := inspectDaemon(paths)
				if err != nil {
					return err
				}
				if !running || record.PID != pid {
					return fmt.Errorf("background process exited before startup completed; check %s", paths.Log)
				}
				fmt.Fprintf(out, "Clivoy started in the background (PID %d) at http://%s\n", record.PID, record.APIAddress)
				fmt.Fprintf(out, "Log: %s\n", record.LogFile)
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("background startup timed out; run clivoy status and check %s", paths.Log)
}

func stopDaemon(paths state.Paths, record *daemonRecord) error {
	if err := controlRequest(record, "/stop"); err != nil {
		return fmt.Errorf("request background shutdown: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		locked, err := instanceLocked(paths)
		if err != nil {
			return err
		}
		if !locked {
			removeDaemonRecord(paths, record)
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("Clivoy did not finish shutdown; check %s and retry clivoy status", record.LogFile)
}

// Management requests never follow redirects or use environment HTTP proxies.
// A stale record must not send its control token to a non-loopback address.
func controlRequest(record *daemonRecord, path string) error {
	u, err := url.Parse(record.ControlURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid local control address")
	}
	method := http.MethodGet
	if path == "/stop" {
		method = http.MethodPost
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, record.ControlURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+record.Token)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("local control connection failed")
	}
	defer resp.Body.Close()
	var identity struct {
		PID       int    `json:"pid"`
		StartedAt string `json:"started_at"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&identity) != nil || identity.PID != record.PID || identity.StartedAt != record.StartedAt {
		return fmt.Errorf("local control identity could not be verified")
	}
	return nil
}

type daemonRuntime struct {
	paths     state.Paths
	record    daemonRecord
	readyPath string
	server    *http.Server
	mu        sync.RWMutex
	started   bool
}

func daemonChild(args []string) (resultErr error) {
	f := flag.NewFlagSet("__daemon", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	readyPath := f.String("ready-file", "", "")
	configPath := f.String("config", "", "")
	listen := f.String("listen", "", "")
	if err := f.Parse(args); err != nil || f.NArg() != 0 {
		return fmt.Errorf("invalid internal daemon arguments")
	}
	paths, err := state.Resolve()
	if err != nil {
		return err
	}
	if filepath.Dir(*readyPath) != paths.Root || !strings.HasPrefix(filepath.Base(*readyPath), ".daemon-start-") {
		return fmt.Errorf("invalid startup handshake path")
	}
	if _, err := privateFile(*readyPath); err != nil {
		return err
	}
	defer func() {
		if resultErr != nil {
			_ = writePrivateJSON(*readyPath, map[string]string{"error": resultErr.Error()})
		}
	}()
	if err := state.Ensure(paths); err != nil {
		return err
	}
	lock, err := daemon.TryLock(filepath.Join(paths.Root, "daemon.lock"))
	if err != nil {
		return fmt.Errorf("acquire background instance lock: %w", err)
	}
	defer lock.Close()
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return err
	}
	actualConfig := *configPath
	if actualConfig == "" {
		actualConfig = paths.Config
	}
	managed := &daemonRuntime{paths: paths, readyPath: *readyPath, record: daemonRecord{
		PID: os.Getpid(), Token: hex.EncodeToString(token[:]), Config: actualConfig,
		Listen: *listen, StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}}
	serveArgs := []string{}
	if *configPath != "" {
		serveArgs = append(serveArgs, "--config", *configPath)
	}
	if *listen != "" {
		serveArgs = append(serveArgs, "--listen", *listen)
	}
	return configuredRuntime("serve", serveArgs, io.Discard, io.Discard, managed)
}

func (d *daemonRuntime) control(cancel context.CancelFunc) error {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen for local daemon control: %w", err)
	}
	d.record.ControlURL = "http://" + listener.Addr().String()
	d.server = &http.Server{ReadHeaderTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, MaxHeaderBytes: 8192}
	d.server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+d.record.Token)) != 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		d.mu.RLock()
		started := d.started
		d.mu.RUnlock()
		if !started {
			http.Error(w, "Starting", http.StatusServiceUnavailable)
			return
		}
		if (r.URL.Path != "/status" || r.Method != http.MethodGet) && (r.URL.Path != "/stop" || r.Method != http.MethodPost) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"pid": d.record.PID, "started_at": d.record.StartedAt})
		if r.URL.Path == "/stop" {
			// Flush the acknowledgement before cancellation closes listeners.
			_ = http.NewResponseController(w).Flush()
			cancel()
		}
	})
	go func() {
		if err := d.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			cancel()
		}
	}()
	return nil
}

func (d *daemonRuntime) ready(address net.Addr) error {
	if _, err := privateFile(d.readyPath); err != nil {
		return fmt.Errorf("startup was abandoned: %w", err)
	}
	d.record.APIAddress = address.String()
	if err := writePrivateJSON(filepath.Join(d.paths.Root, "daemon.json"), d.record); err != nil {
		return fmt.Errorf("save background state: %w", err)
	}
	d.mu.Lock()
	d.started = true
	d.mu.Unlock()
	return writePrivateJSON(d.readyPath, map[string]bool{"ready": true})
}

func (d *daemonRuntime) close() {
	if d.server != nil {
		_ = d.server.Close()
	}
	removeDaemonRecord(d.paths, &d.record)
}

func readDaemonRecord(paths state.Paths) (*daemonRecord, error) {
	var record daemonRecord
	if err := readPrivateJSON(filepath.Join(paths.Root, "daemon.json"), &record); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read background state: %w", err)
	}
	if record.PID <= 0 || len(record.Token) != 64 || !filepath.IsAbs(record.Config) || record.StartedAt == "" {
		return nil, fmt.Errorf("invalid background state; inspect %s", filepath.Join(paths.Root, "daemon.json"))
	}
	return &record, nil
}

func removeDaemonRecord(paths state.Paths, record *daemonRecord) {
	if record == nil {
		return
	}
	current, err := readDaemonRecord(paths)
	if err == nil && current != nil && current.Token == record.Token {
		_ = os.Remove(filepath.Join(paths.Root, "daemon.json"))
	}
}

func privateFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, fmt.Errorf("%s must be a private regular file (0600), not a symlink", path)
	}
	if info.Size() > 64<<10 {
		return nil, fmt.Errorf("background state file is too large")
	}
	return info, nil
}

func readPrivateJSON(path string, value any) error {
	before, err := privateFile(path)
	if err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return fmt.Errorf("background state changed while opening")
	}
	decoder := json.NewDecoder(io.LimitReader(file, (64<<10)+1))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("background state must contain one JSON object")
	}
	return nil
}

func writePrivateJSON(path string, value any) error {
	if _, err := privateFile(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var data bytes.Buffer
	if err := json.NewEncoder(&data).Encode(value); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".daemon-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data.Bytes()); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	// Windows readers opened by os.Open don't share deletion. A concurrent
	// status/start poll can briefly prevent replacing its state file. Preserve
	// the old file and retry only transient Windows sharing/access errors.
	deadline := time.Now().Add(time.Second)
	for {
		err := os.Rename(file.Name(), path)
		if runtime.GOOS != "windows" || err == nil || time.Now().After(deadline) ||
			(!errors.Is(err, syscall.Errno(5)) && !errors.Is(err, syscall.Errno(32)) && !errors.Is(err, syscall.Errno(33))) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
