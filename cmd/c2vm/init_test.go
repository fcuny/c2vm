package main

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestGenerateInitScript(t *testing.T) {
	script, err := generateInitScript(ocispec.ImageConfig{
		Env:        []string{"PATH=/usr/local/bin:/usr/bin:/bin", "GREETING=hello world"},
		Entrypoint: []string{"/docker-entrypoint.sh"},
		Cmd:        []string{"nginx", "-g", "daemon off;"},
		WorkingDir: "/srv/app",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		"export 'GREETING=hello world'\n",
		"cd '/srv/app' || exit 1\n",
		"exec '/docker-entrypoint.sh' 'nginx' '-g' 'daemon off;'\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script is missing %q:\n%s", want, script)
		}
	}
}

func TestGenerateInitScriptNoCommand(t *testing.T) {
	if _, err := generateInitScript(ocispec.ImageConfig{}); err == nil {
		t.Fatal("expected an error for an image without a command")
	}
}

func TestShellQuote(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}

	for _, s := range []string{"", "plain", "two words", "it's", `"double"`, "$HOME", "`date`", "a\nb", `back\slash`} {
		out, err := exec.Command(sh, "-c", "printf %s "+shellQuote(s)).Output()
		if err != nil {
			t.Fatalf("sh -c failed for %q: %v", s, err)
		}
		if string(out) != s {
			t.Errorf("shellQuote(%q) round-tripped as %q", s, out)
		}
	}
}

func TestGenerateInitScriptRuns(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}

	dir := filepath.Join(t.TempDir(), "work dir")
	script, err := generateInitScript(ocispec.ImageConfig{
		Env:        []string{"GREETING=it's a test"},
		Cmd:        []string{"sh", "-c", `printf '%s|%s' "$GREETING" "$PWD"`},
		WorkingDir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Drop the mounts: they need root and aren't what's under test.
	var lines []string
	for _, line := range strings.Split(script, "\n") {
		if !strings.HasPrefix(line, "mount ") && !strings.HasPrefix(line, "mkdir -p /proc") {
			lines = append(lines, line)
		}
	}

	out, err := exec.Command(sh, "-c", strings.Join(lines, "\n")).Output()
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, script)
	}
	if want := "it's a test|" + dir; string(out) != want {
		t.Errorf("got %q, want %q", out, want)
	}
}
