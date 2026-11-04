package main

import (
	"runtime/debug"
	"strings"
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

func TestInitVersion(t *testing.T) {
	if got, want := initVersion(nil), "c2vm-init: not built in (build c2vm with make, or boot with -init)"; got != want {
		t.Errorf("initVersion(nil) = %q, want %q", got, want)
	}
	if got := initVersion([]byte("not a Go binary")); !strings.HasPrefix(got, "c2vm-init: built in, version unknown (") {
		t.Errorf("initVersion(not a Go binary) = %q", got)
	}
}
