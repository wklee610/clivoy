package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/wklee610/clivoy/internal/config"
	"github.com/wklee610/clivoy/internal/modelcatalog"
)

// Inspection commands work before init without creating config or credentials.
func inspectionConfig(path string) (config.Config, error) {
	c, err := config.Load(path)
	if path == "" && errors.Is(err, os.ErrNotExist) {
		return config.Default(), nil
	}
	return c, err
}

func providerNames(c config.Config) []string {
	names := make([]string, 0, len(c.Providers))
	for name := range c.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Keep an HTTP backend's key out of unrelated subprocesses. A provider's own
// native API-key login still works when its HTTP connection is also configured.
// The gateway credential is always excluded, even if given a native key name.
func excludedCLIEnvironment(c config.Config, kind string) []string {
	excluded := []string{c.APIKeyEnv}
	for _, p := range c.Providers {
		key := strings.ToUpper(p.APIKeyEnv)
		if kind == "grok" && key == "XAI_API_KEY" || kind == "gemini" && (key == "GEMINI_API_KEY" || key == "GOOGLE_API_KEY") {
			continue
		}
		if p.APIKeyEnv != "" {
			excluded = append(excluded, p.APIKeyEnv)
		}
	}
	return excluded
}

func configuredModels(c config.Config) map[string]config.Model {
	models, _ := modelChoices(c, false)
	return models
}

func modelChoices(c config.Config, refresh bool) (map[string]config.Model, []string) {
	return modelChoicesWithDiscovery(c, refresh, modelcatalog.Refresh)
}

type cliModelDiscovery func(context.Context, string, string, ...string) ([]modelcatalog.Model, error)

func modelChoicesWithDiscovery(c config.Config, refresh bool, discover cliModelDiscovery) (map[string]config.Model, []string) {
	models := make(map[string]config.Model, len(c.Models))
	for name, m := range c.Models {
		models[name] = m
	}
	var warnings []string
	// Bound total Ollama query time without charging intervening CLI discovery.
	ollamaBudget := 2 * time.Second
	for _, name := range providerNames(c) {
		p := c.Providers[name]
		if !p.SupportsNativeModels() {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		var available []modelcatalog.Model
		var err error
		if p.Type == "ollama" {
			key := ""
			if p.APIKeyEnv != "" {
				key = os.Getenv(p.APIKeyEnv)
			}
			if p.APIKeyEnv != "" && key == "" {
				err = fmt.Errorf("environment variable %s is empty", p.APIKeyEnv)
			} else {
				started := time.Now()
				ollamaCtx, cancelOllama := context.WithTimeout(ctx, ollamaBudget)
				available, err = modelcatalog.DiscoverOllama(ollamaCtx, p.BaseURL, key)
				cancelOllama()
				ollamaBudget -= time.Since(started)
			}
		} else if refresh {
			available, err = discover(ctx, p.Type, p.Command.Executable(), excludedCLIEnvironment(c, p.Type)...)
		} else {
			available, err = modelcatalog.Discover(ctx, p.Type, p.Command.Executable())
		}
		cancel()
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		for _, m := range available {
			id := name + "/" + m.ID
			if _, explicit := models[id]; !explicit {
				models[id] = config.Model{Provider: name, Model: m.ID, Capabilities: m.Capabilities}
			}
		}
	}
	return models, warnings
}

func listModels(args []string, out, errOut io.Writer) error {
	return listModelsWithDiscovery(args, out, errOut, modelcatalog.Refresh)
}

func listModelsWithDiscovery(args []string, out, errOut io.Writer, discover cliModelDiscovery) error {
	f := flag.NewFlagSet("models", flag.ContinueOnError)
	f.SetOutput(errOut)
	path := f.String("config", "", "configuration file (defaults are used before init)")
	f.Bool("refresh", false, "query current model metadata (already the default; retained for compatibility)")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	c, err := inspectionConfig(*path)
	if err != nil {
		return err
	}
	models, warnings := modelChoicesWithDiscovery(c, true, discover)
	for _, warning := range warnings {
		fmt.Fprintln(errOut, warning)
	}
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "MODEL\tPROVIDER\tCLI / UPSTREAM MODEL\tSOURCE")
	for _, name := range names {
		m := models[name]
		upstream := m.Model
		if upstream == "" {
			upstream = "(CLI default)"
		}
		source := "CLI"
		if _, configured := c.Models[name]; configured {
			source = "config"
		} else if c.Providers[m.Provider].Type == "ollama" {
			source = "server"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, m.Provider, upstream, source)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(out, "\nSOURCE: CLI = returned by the CLI; server = returned by Ollama; config = configured alias (not verified).")
	fmt.Fprintln(out, "CLI catalogs may use their own caches. Listing models does not verify account access.")
	fmt.Fprintln(out, "Use <provider>/<model-id> to select a model; pull Ollama models separately.")
	return nil
}
