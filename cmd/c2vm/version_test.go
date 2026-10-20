package main

import (
	"runtime/debug"
	"testing"
)

func TestModuleVersion(t *testing.T) {
	for _, tt := range []struct {
		version string
		want    string
	}{
		{"v0.1.0", "v0.1.0"},
		{"v0.1.1-0.20261019000000-99f91ff0a1b2+dirty", "v0.1.1-0.20261019000000-99f91ff0a1b2+dirty"},
		{"(devel)", "devel"},
		{"", "devel"},
	} {
		bi := &debug.BuildInfo{Main: debug.Module{Version: tt.version}}
		if got := moduleVersion(bi); got != tt.want {
			t.Errorf("moduleVersion(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}
