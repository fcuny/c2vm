//go:build embedinit

package main

import (
	"strings"
	"testing"
)

// TestEmbeddedInit checks the init make build embedded is one the VM
// can run, and that c2vm version can tell which it is. CI runs it after
// make build.
func TestEmbeddedInit(t *testing.T) {
	if err := checkInit(embeddedInit); err != nil {
		t.Errorf("the c2vm-init built into c2vm %v", err)
	}
	if got := initVersion(embeddedInit); !strings.HasPrefix(got, "c2vm-init ") || !strings.HasSuffix(got, " (built in, linux/arm64)") {
		t.Errorf("initVersion = %q, want c2vm-init <version> (built in, linux/arm64)", got)
	}
}
