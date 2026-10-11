//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDaemonStateReplacementWaitsForWindowsReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := writePrivateJSON(path, map[string]int{"generation": 1}); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	done := make(chan error, 1)
	go func() { done <- writePrivateJSON(path, map[string]int{"generation": 2}) }()
	select {
	case err := <-done:
		t.Fatalf("replacement finished before reader closed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not complete after reader closed")
	}
	var value map[string]int
	if err := readPrivateJSON(path, &value); err != nil || value["generation"] != 2 {
		t.Fatalf("replacement was not published: %v, %v", value, err)
	}
}
