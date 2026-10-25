//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLoadInitFIFO checks a FIFO given as -init is refused rather than
// opened, which blocks until something writes to it.
func TestLoadInitFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fifo")
	if err := syscall.Mkfifo(path, 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := loadInit(path)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Errorf("error = %v, want %q", err, "not a regular file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loadInit still blocked on a FIFO after 5s")
	}
}
