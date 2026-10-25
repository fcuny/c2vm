// Package e2e boots VMs with c2vm and checks what the commands in them
// see and do. It only runs with C2VM_E2E=1, on macOS on Apple silicon,
// as it needs Virtualization.framework and the network:
//
//	C2VM_E2E=1 go test -count=1 -timeout 30m ./e2e
//
// or make e2e.
package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// image is what the tests boot: small, with a shell and coreutils from
// busybox. It's pinned so a new release doesn't change the output. Every
// boot fetches its manifest, so it comes from ECR's mirror of Docker's
// official images: a few runs would use up Docker Hub's anonymous limit.
const image = "public.ecr.aws/docker/library/alpine:3.24"

// timeout is how long a VM may run before the test gives up on it.
const timeout = 2 * time.Minute

var (
	// skip is why the tests don't run, if they don't.
	skip string
	// c2vm is the binary built for the tests.
	c2vm string
)

func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	switch {
	case os.Getenv("C2VM_E2E") == "":
		skip = "set C2VM_E2E=1 to boot VMs"
		return m.Run()
	case runtime.GOOS != "darwin" || runtime.GOARCH != "arm64":
		skip = "booting VMs needs macOS on Apple silicon"
		return m.Run()
	}

	dir, err := os.MkdirTemp("", "c2vm-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(dir)

	c2vm = filepath.Join(dir, "c2vm")
	if err := build(c2vm); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Pull the kernel and the image once, before the tests boot VMs in
	// parallel and would all fetch them at the same time.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	warm := exec.CommandContext(ctx, c2vm, "boot", image, "--", "true")
	// SIGTERM lets c2vm remove its files, which SIGKILL wouldn't.
	warm.Cancel = func() error { return warm.Process.Signal(syscall.SIGTERM) }
	warm.WaitDelay = 10 * time.Second
	if out, err := warm.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "booting %s: %v\n%s", image, err, out)
		return 1
	}

	return m.Run()
}

// build builds c2vm as make build does, so the tests boot what ships:
// c2vm-init for Linux, on arm64 whatever GOARCH says, as that's the only
// architecture VMs can run, then c2vm with it embedded, signed with the
// entitlement to create VMs.
func build(c2vm string) error {
	cmds := []*exec.Cmd{
		exec.Command("go", "build", "-o", "../cmd/c2vm/c2vm-init", "../cmd/c2vm-init"),
		exec.Command("go", "build", "-tags", "embedinit", "-o", c2vm, "../cmd/c2vm"),
		exec.Command("codesign", "--force", "--sign", "-", "--entitlements", "../hack/vz.entitlements", c2vm),
	}
	cmds[0].Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64")
	for _, cmd := range cmds {
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v\n%s", strings.Join(cmd.Args, " "), err, out)
		}
	}
	return nil
}

// bootCmd returns the command to boot a VM with c2vm boot and args. It's
// stopped, as with Ctrl-C, if it runs for longer than timeout.
func bootCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	if skip != "" {
		t.Skip(skip)
	}

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, c2vm, append([]string{"boot"}, args...)...)
	// The context also ends with the test: only a timeout is an error.
	cmd.Cancel = func() error {
		if ctx.Err() == context.DeadlineExceeded {
			t.Errorf("c2vm still running after %v, stopping it", timeout)
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

// boot runs cmd, from bootCmd, and returns what the VM printed on its
// console, without init's messages, and c2vm's exit status.
func boot(t *testing.T, cmd *exec.Cmd) (string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("c2vm's console:\n%s\nc2vm's stderr:\n%s", stdout.String(), stderr.String())
		}
	})

	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("running c2vm: %v", err)
	}
	return commandOutput(stdout.String()), cmd.ProcessState.ExitCode()
}

// commandOutput returns what the command printed on the console: out
// without init's messages, and with \n line endings, which the console's
// terminal turns into \r\n. A message follows the command's output on
// the same line when that doesn't end with a newline.
func commandOutput(out string) string {
	var b strings.Builder
	for line := range strings.Lines(strings.ReplaceAll(out, "\r\n", "\n")) {
		if before, _, found := strings.Cut(line, "c2vm-init: "); found {
			line = before
		}
		b.WriteString(line)
	}
	return b.String()
}

// hasLine reports whether line is one of out's lines.
func hasLine(out, line string) bool {
	for l := range strings.Lines(out) {
		if strings.TrimSuffix(l, "\n") == line {
			return true
		}
	}
	return false
}

func TestCommand(t *testing.T) {
	t.Parallel()
	out, code := boot(t, bootCmd(t, image, "--", "uname", "-sm"))
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if out != "Linux aarch64\n" {
		t.Errorf("output %q, want %q", out, "Linux aarch64\n")
	}
}

func TestExitStatus(t *testing.T) {
	t.Parallel()
	_, code := boot(t, bootCmd(t, image, "--", "sh", "-c", "exit 3"))
	if code != 3 {
		t.Errorf("exit status %d, want 3", code)
	}
}

func TestEnv(t *testing.T) {
	t.Parallel()
	cmd := bootCmd(t, "-e", "FOO=foo", "-e", "BAR", "-e", "UNSET", image, "--", "sh", "-c", `echo "$FOO $BAR ${UNSET-unset}"`)
	// UNSET mustn't be there for c2vm to copy, whatever the shell has.
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "UNSET=") })
	cmd.Env = append(env, "BAR=bar")
	out, code := boot(t, cmd)
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if want := "foo bar unset\n"; out != want {
		t.Errorf("output %q, want %q", out, want)
	}
}

func TestStdin(t *testing.T) {
	t.Parallel()
	cmd := bootCmd(t, "-i", image, "--", "cat")
	cmd.Stdin = strings.NewReader("hello\nworld\n")
	out, code := boot(t, cmd)
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if want := "hello\nworld\n"; out != want {
		t.Errorf("output %q, want %q", out, want)
	}
}

func TestStdinBinary(t *testing.T) {
	t.Parallel()
	data := make([]byte, 8<<20)
	rand.Read(data)
	sum := sha256.Sum256(data)

	cmd := bootCmd(t, "-i", image, "--", "sha256sum")
	cmd.Stdin = bytes.NewReader(data)
	out, code := boot(t, cmd)
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if want := hex.EncodeToString(sum[:]) + "  -\n"; out != want {
		t.Errorf("output %q, want %q", out, want)
	}
}

func TestNoStdin(t *testing.T) {
	t.Parallel()
	// Without -i, cat reads /dev/null, and not c2vm's stdin: it exits
	// right away, though the pipe is never closed.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err := io.WriteString(w, "not for cat\n"); err != nil {
		t.Fatal(err)
	}

	cmd := bootCmd(t, image, "--", "cat")
	cmd.Stdin = r
	out, code := boot(t, cmd)
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if out != "" {
		t.Errorf("output %q, want nothing", out)
	}
}

func TestVolume(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("from the host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := boot(t, bootCmd(t, "-v", dir+":/data", image, "--", "sh", "-c", "cat /data/in && echo 'from the guest' > /data/out"))
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if want := "from the host\n"; out != want {
		t.Errorf("output %q, want %q", out, want)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "out")); err != nil || string(got) != "from the guest\n" {
		t.Errorf("the guest wrote %q (%v), want %q", got, err, "from the guest\n")
	}
}

func TestVolumeReadOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in"), []byte("from the host\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, code := boot(t, bootCmd(t, "-v", dir+":/data:ro", image, "--", "sh", "-c", "cat /data/in && echo 'from the guest' > /data/out"))
	if code == 0 {
		t.Error("exit status 0, want the write to fail")
	}
	if !strings.HasPrefix(out, "from the host\n") {
		t.Errorf("output %q, want the file of the host first", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "out")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the guest wrote to a read-only volume: %v", err)
	}
}

func TestVolumeNonRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	out, code := boot(t, bootCmd(t, "-v", dir+":/data", image, "--", "su", "-s", "/bin/sh", "-c", "id -un && echo 'from nobody' > /data/out", "nobody"))
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if want := "nobody\n"; out != want {
		t.Errorf("output %q, want %q", out, want)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "out")); err != nil || string(got) != "from nobody\n" {
		t.Errorf("the guest wrote %q (%v), want %q", got, err, "from nobody\n")
	}
}

// terminal is c2vm running with a pty as its terminal.
type terminal struct {
	t   *testing.T
	cmd *exec.Cmd
	pty *os.File

	mu   sync.Mutex
	out  bytes.Buffer
	done chan struct{}
	// waited is whether wait ran.
	waited bool
}

// startTerminal starts cmd, from bootCmd, with a pty of size as its
// terminal.
func startTerminal(t *testing.T, cmd *exec.Cmd, size pty.Winsize) *terminal {
	t.Helper()
	f, err := pty.StartWithSize(cmd, &size)
	if err != nil {
		t.Fatal(err)
	}
	term := &terminal{t: t, cmd: cmd, pty: f, done: make(chan struct{})}
	go func() {
		defer close(term.done)
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			term.mu.Lock()
			term.out.Write(buf[:n])
			term.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		// The test stopped before waiting for c2vm: its context is
		// done, so it was sent SIGTERM, and is killed if it doesn't stop
		// in time. Wait for it, not to leave it behind.
		if !term.waited {
			cmd.Wait()
		}
		f.Close()
		if t.Failed() {
			t.Logf("c2vm's terminal:\n%s", term.output())
		}
	})
	return term
}

func (term *terminal) output() string {
	term.mu.Lock()
	defer term.mu.Unlock()
	return term.out.String()
}

// waitFor waits for s to show up on the terminal.
func (term *terminal) waitFor(s string) {
	term.t.Helper()
	deadline := time.Now().Add(timeout)
	for !strings.Contains(term.output(), s) {
		if time.Now().After(deadline) {
			term.t.Fatalf("%q never showed up on the terminal", s)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// wait waits for c2vm to exit, and returns what showed up on the
// terminal and c2vm's exit status.
func (term *terminal) wait() (string, int) {
	term.t.Helper()
	term.waited = true
	err := term.cmd.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		term.t.Fatalf("running c2vm: %v", err)
	}
	// Once c2vm has exited, nothing has the pty open but us, and reading
	// it ends.
	select {
	case <-term.done:
	case <-time.After(5 * time.Second):
		term.t.Fatal("the terminal's output didn't end after c2vm exited")
	}
	return strings.ReplaceAll(term.output(), "\r\n", "\n"), term.cmd.ProcessState.ExitCode()
}

func TestTTY(t *testing.T) {
	t.Parallel()
	term := startTerminal(t, bootCmd(t, "-t", image, "--", "sh", "-c", "tty && stty size && exit 4"), pty.Winsize{Rows: 24, Cols: 80})
	out, code := term.wait()
	if code != 4 {
		t.Errorf("exit status %d, want 4", code)
	}
	if !hasLine(out, "/dev/hvc0") {
		t.Errorf("output %q, want the console as the terminal", out)
	}
	if !hasLine(out, "24 80") {
		t.Errorf("output %q, want the terminal's size, 24 80", out)
	}
}

func TestTTYResize(t *testing.T) {
	t.Parallel()
	// The shell prints the size when the terminal's changes, which only
	// happens when c2vm sends it. It gives up after 10s.
	script := `trap 'stty size; exit 5' WINCH; echo ready; for i in $(seq 100); do sleep 0.1; done; exit 6`
	term := startTerminal(t, bootCmd(t, "-t", image, "--", "sh", "-c", script), pty.Winsize{Rows: 24, Cols: 80})
	term.waitFor("ready")
	if err := pty.Setsize(term.pty, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	out, code := term.wait()
	if code != 5 {
		t.Errorf("exit status %d, want 5", code)
	}
	if !hasLine(out, "30 100") {
		t.Errorf("output %q, want the new size, 30 100", out)
	}
}

func TestTTYEscape(t *testing.T) {
	t.Parallel()
	term := startTerminal(t, bootCmd(t, "-t", image, "--", "sh", "-c", "echo ready; sleep 600"), pty.Winsize{Rows: 24, Cols: 80})
	term.waitFor("ready")
	// Ctrl-] stops the VM, as if c2vm was interrupted.
	if _, err := term.pty.Write([]byte{0x1d}); err != nil {
		t.Fatal(err)
	}
	if _, code := term.wait(); code != 128+int(syscall.SIGINT) {
		t.Errorf("exit status %d, want %d", code, 128+int(syscall.SIGINT))
	}
}

func TestTTYRequiresTerminal(t *testing.T) {
	t.Parallel()
	cmd := bootCmd(t, "-t", image, "--", "true")
	cmd.Stdin = strings.NewReader("")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("c2vm boot -t succeeded without a terminal")
	}
	if !strings.Contains(stderr.String(), "-t needs a terminal") {
		t.Errorf("stderr %q, want it to say -t needs a terminal", stderr.String())
	}
}
