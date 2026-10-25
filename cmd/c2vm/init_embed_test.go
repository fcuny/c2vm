//go:build embedinit

package main

import "testing"

// TestEmbeddedInit checks the init make build embedded is one the VM
// can run. CI runs it after make build.
func TestEmbeddedInit(t *testing.T) {
	if err := checkInit(embeddedInit); err != nil {
		t.Errorf("the c2vm-init built into c2vm %v", err)
	}
}
