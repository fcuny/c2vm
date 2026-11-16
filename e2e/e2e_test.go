// Package e2e boots VMs with c2vm and checks what the commands in them
// see and do. It only runs with C2VM_E2E=1, on macOS on Apple silicon,
// as it needs Virtualization.framework and the network:
//
//	C2VM_E2E=1 go test -count=1 -timeout 30m ./e2e
//
// or make e2e. The VMs boot c2vm's default kernel, or C2VM_E2E_KERNEL:
// an image, or by its absolute path a directory laid out like one, or a
// kernel file. A kernel file has no mkfs.ext4, so the -scratch tests are
// skipped with one.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// imageTag is what the tests boot: small, with a shell and coreutils
// from busybox. It's pinned so a new release doesn't change the output.
// Only the first boot looks it up: the tests boot it by digest, from
// c2vm's cache. It comes from ECR's mirror of Docker's official images
// anyway, as Docker Hub's anonymous limit is lower.
const imageTag = "public.ecr.aws/docker/library/alpine:3.24"

// timeout is how long a VM may run before the test gives up on it.
const timeout = 2 * time.Minute

var (
	// skip is why the tests don't run, if they don't.
	skip string
	// c2vm and c2vmInit are the binaries built for the tests, c2vm with
	// c2vmInit embedded.
	c2vm, c2vmInit string
	// bootArgs come first in every c2vm boot.
	bootArgs []string
	// image is imageTag by the digest it resolved to.
	image string
	// guestTools is a directory of helpers built for the guest, which
	// the tests mount when the image doesn't have what they need.
	guestTools string
	// scratchSkip is why the -scratch tests don't run, if they don't.
	scratchSkip string
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

	c2vm, c2vmInit = filepath.Join(dir, "c2vm"), filepath.Join(dir, "c2vm-init")
	guestTools = filepath.Join(dir, "guest")
	if err := build(c2vm, c2vmInit, guestTools); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	kernel := os.Getenv("C2VM_E2E_KERNEL")
	if kernel != "" {
		if err := checkKernel(kernel); err != nil {
			fmt.Fprintf(os.Stderr, "C2VM_E2E_KERNEL: %v\n", err)
			return 1
		}
		bootArgs = []string{"-kernel", kernel}
	}

	// Pull the kernel and the image once, before the tests boot VMs in
	// parallel and would all fetch them at the same time.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	warm := exec.CommandContext(ctx, c2vm, slices.Concat([]string{"boot"}, bootArgs, []string{imageTag, "--", "true"})...)
	// SIGTERM lets c2vm remove its files, which SIGKILL wouldn't.
	warm.Cancel = func() error { return warm.Process.Signal(syscall.SIGTERM) }
	warm.WaitDelay = 10 * time.Second
	out, err := warm.CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "booting %s: %v\n%s", imageTag, err, out)
		return 1
	}

	// From now on, boot the image and the kernel by the digests their
	// tags resolved to, which c2vm finds in its cache without asking the
	// registry: a run of the tests looks each tag up once.
	resolved := resolvedDigests(string(out))
	digest, ok := resolved[imageTag]
	if !ok {
		fmt.Fprintf(os.Stderr, "booting %s: c2vm didn't log what it resolved to\n%s", imageTag, out)
		return 1
	}
	image = pinned(imageTag, digest)
	delete(resolved, imageTag)
	// What's left is the kernel, unless it's a file.
	switch len(resolved) {
	case 0:
	case 1:
		for ref, digest := range resolved {
			bootArgs = []string{"-kernel", pinned(ref, digest)}
		}
	default:
		fmt.Fprintf(os.Stderr, "booting %s: c2vm resolved more than the image and the kernel\n%s", imageTag, out)
		return 1
	}

	scratchSkip = noScratch(kernel)
	return m.Run()
}

var (
	// resolvedRE matches c2vm's log line for a reference it resolved,
	// from the registry or from its cache.
	resolvedRE = regexp.MustCompile(`(?m)^c2vm: resolved (\S+) to (sha256:[0-9a-f]{64})(?:, from the cache)?\r?$`)
	// fallbackRE matches its line for a tag it couldn't look up, and
	// boots by the digest it last resolved to.
	fallbackRE = regexp.MustCompile(`(?m)^c2vm: using (sha256:[0-9a-f]{64}), which (\S+) resolved to on `)
)

// resolvedDigests returns the digest each reference c2vm looked up
// resolved to, from what it logged.
func resolvedDigests(log string) map[string]string {
	digests := map[string]string{}
	for _, m := range resolvedRE.FindAllStringSubmatch(log, -1) {
		digests[m[1]] = m[2]
	}
	for _, m := range fallbackRE.FindAllStringSubmatch(log, -1) {
		digests[m[2]] = m[1]
	}
	return digests
}

// pinned returns ref, by tag or by digest, as a reference to digest.
func pinned(ref, digest string) string {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref = ref[:i]
	} else if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		ref = ref[:i]
	}
	return ref + "@" + digest
}

// checkKernel fails if kernel, which c2vm boot -kernel takes, would be
// the wrong file or image. c2vm boots a file if there's one, and pulls
// an image otherwise, but the tests run from e2e/, not from where make or
// go test was run: a relative path names another file there, or nothing,
// and then an image. So a file must be an absolute path, and exist.
func checkKernel(kernel string) error {
	if filepath.IsAbs(kernel) {
		if _, err := os.Stat(kernel); err != nil {
			return err
		}
		return nil
	}
	if strings.HasPrefix(kernel, ".") {
		return fmt.Errorf("%s: use an absolute path", kernel)
	}
	// Most likely relative to the top of the repository.
	if _, err := os.Stat(filepath.Join("..", kernel)); err == nil {
		return fmt.Errorf("%s: use an absolute path", kernel)
	}
	return nil
}

// noScratch returns why the -scratch tests can't run with kernel,
// C2VM_E2E_KERNEL, if they can't: a kernel file has no mkfs.ext4. There's
// no warm-up for them: the boot that pulls the kernel image caches its
// mkfs.ext4 too. Only a kernel cached before that, without it, makes
// them each pull the image again.
func noScratch(kernel string) string {
	if fi, err := os.Stat(kernel); kernel != "" && err == nil && fi.Mode().IsRegular() {
		return "C2VM_E2E_KERNEL is a kernel file, without mkfs.ext4: pass a directory laid out like the kernel image to test -scratch"
	}
	return ""
}

// scratchCmd is bootCmd for the -scratch tests, which it skips when
// noScratch says so.
func scratchCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	if scratchSkip != "" {
		t.Skip(scratchSkip)
	}
	return bootCmd(t, args...)
}

// build builds c2vm as make build does, so the tests boot what ships:
// c2vm-init for Linux on arm64, then c2vm with it embedded, signed with
// the entitlement to create VMs. An overlay embeds c2vmInit from where
// it's built, so the tests leave the checkout alone. It also builds the
// helpers in testdata into guestTools, for the guest.
func build(c2vm, c2vmInit, guestTools string) error {
	dir := filepath.Dir(c2vm)
	embedPath, err := filepath.Abs("../cmd/c2vm/c2vm-init")
	if err != nil {
		return err
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": {embedPath: c2vmInit}})
	if err != nil {
		return err
	}
	overlayPath := filepath.Join(dir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o644); err != nil {
		return err
	}

	cmds := []*exec.Cmd{
		exec.Command("go", "build", "-o", c2vmInit, "../cmd/c2vm-init"),
		exec.Command("go", "build", "-tags", "embedinit", "-overlay", overlayPath, "-o", c2vm, "../cmd/c2vm"),
		exec.Command("codesign", "--force", "--sign", "-", "--entitlements", "../hack/vz.entitlements", c2vm),
		exec.Command("go", "build", "-o", filepath.Join(guestTools, "xattr"), "./testdata/xattr"),
	}
	cmds[0].Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64")
	cmds[3].Env = cmds[0].Env
	for _, cmd := range cmds {
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %v\n%s", strings.Join(cmd.Args, " "), err, out)
		}
	}
	return nil
}

// bootCmd returns the command to boot a VM with c2vm boot and args. It's
// stopped with SIGQUIT if it runs for longer than timeout, so its stderr
// shows where it was stuck.
func bootCmd(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	if skip != "" {
		t.Skip(skip)
	}

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, c2vm, slices.Concat([]string{"boot"}, bootArgs, args)...)
	// The context also ends with the test: only a timeout is an error.
	cmd.Cancel = func() error {
		if ctx.Err() == context.DeadlineExceeded {
			t.Errorf("c2vm still running after %v, stopping it with SIGQUIT", timeout)
			// Go prints every goroutine's stack and exits, without
			// removing c2vm's files as SIGTERM would.
			return cmd.Process.Signal(syscall.SIGQUIT)
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 10 * time.Second
	return cmd
}

// boot runs cmd, from bootCmd, and returns c2vm's stdout, which is the
// command's, byte for byte, without -t, and c2vm's exit status.
func boot(t *testing.T, cmd *exec.Cmd) (string, int) {
	t.Helper()
	stdout, stderr, code := bootOutputs(t, cmd)
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("c2vm's stdout:\n%s\nc2vm's stderr:\n%s", stdout, stderr)
		}
	})
	return stdout, code
}

// bootOutputs runs cmd, from bootCmd, and returns c2vm's stdout and
// stderr, and its exit status.
func bootOutputs(t *testing.T, cmd *exec.Cmd) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()

	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("running c2vm: %v", err)
	}
	return stdout.String(), stderr.String(), cmd.ProcessState.ExitCode()
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

// TestInitFlag boots the init given with -init rather than the one built
// into c2vm. They're the same binary, so this checks the path to it.
func TestInitFlag(t *testing.T) {
	t.Parallel()
	_, code := boot(t, bootCmd(t, "-init", c2vmInit, image, "--", "sh", "-c", "exit 3"))
	if code != 3 {
		t.Errorf("exit status %d, want 3", code)
	}
}

// TestKernelPanic checks a guest kernel panic fails the run, instead of
// leaving the VM waiting forever, or rebooting and running the command
// again.
func TestKernelPanic(t *testing.T) {
	t.Parallel()
	// A hang ends with bootCmd's timeout, without the message.
	stdout, stderr, code := bootOutputs(t, bootCmd(t, image, "--", "sh", "-c", "echo c > /proc/sysrq-trigger"))
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("c2vm's stdout:\n%s\nc2vm's stderr:\n%s", stdout, stderr)
		}
	})
	if code == 0 {
		t.Error("exit status 0, want non-zero")
	}
	if !strings.Contains(stderr, "c2vm: the guest kernel panicked") {
		t.Error("stderr doesn't say the guest kernel panicked")
	}
	if !strings.Contains(stderr, "Kernel panic - not syncing: sysrq triggered crash") {
		t.Error("stderr doesn't have the kernel's panic message")
	}
	if stdout != "" {
		t.Errorf("stdout %q, want nothing: the kernel's messages go to stderr", stdout)
	}
}

// TestCommandPrintsPanic checks the command's output can't pass for a
// kernel panic: only the kernel's console is watched.
func TestCommandPrintsPanic(t *testing.T) {
	t.Parallel()
	script := `echo "[    0.820843] Kernel panic - not syncing: not really"; ` +
		`echo "---[ end Kernel panic - not syncing: not really ]---"; sleep 2; exit 7`
	out, code := boot(t, bootCmd(t, image, "--", "sh", "-c", script))
	if code != 7 {
		t.Errorf("exit status %d, want 7", code)
	}
	if !strings.HasSuffix(out, "not really ]---\n") {
		t.Errorf("output %q, want the command's whole output", out)
	}
}

// TestPanicOnOops checks an oops panics the kernel: it kills the task
// that hit it, and leaves the kernel in a state the run can't trust.
func TestPanicOnOops(t *testing.T) {
	t.Parallel()
	out, code := boot(t, bootCmd(t, image, "--", "cat", "/proc/sys/kernel/panic_on_oops"))
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if out != "1\n" {
		t.Errorf("panic_on_oops is %q, want %q", out, "1\n")
	}
}

// TestInitInvalid checks c2vm refuses an init the VM can't run, which
// made the kernel panic and the VM hang.
func TestInitInvalid(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "c2vm-init")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := bootCmd(t, "-init", path, image, "--", "true")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("c2vm boot succeeded with an init that isn't an ELF binary")
	}
	if !strings.Contains(stderr.String(), path+" is not an ELF binary") {
		t.Errorf("stderr %q, want it to say %s is not an ELF binary", stderr.String(), path)
	}
}

// TestOffline boots the image by tag with the registry out of reach:
// c2vm boots what the tag resolved to before, and warns it may be out of
// date.
func TestOffline(t *testing.T) {
	t.Parallel()
	cmd := bootCmd(t, imageTag, "--", "true")
	// The kernel is booted by digest, from the cache, so only the tag's
	// lookup goes through the proxy, which refuses the connection.
	proxy := "http://127.0.0.1:1"
	cmd.Env = append(os.Environ(), "HTTPS_PROXY="+proxy, "HTTP_PROXY="+proxy, "NO_PROXY=", "no_proxy=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("c2vm boot with the registry out of reach: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "may be out of date") {
		t.Errorf("no warning that the image may be out of date:\n%s", stderr.String())
	}
}

// printMAC is the command that prints the VM's MAC address.
var printMAC = []string{"cat", "/sys/class/net/eth0/address"}

// bootMAC boots a VM, with env added to c2vm's environment, and returns
// its MAC address.
func bootMAC(t *testing.T, env ...string) string {
	t.Helper()
	cmd := bootCmd(t, slices.Concat([]string{image, "--"}, printMAC)...)
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, code := boot(t, cmd)
	if code != 0 {
		t.Fatalf("exit status %d, want 0", code)
	}
	return strings.TrimSpace(out)
}

// TestMACReused checks a VM gets the MAC address of the VM before it,
// and so its DHCP lease, rather than taking a new lease from vmnet's
// pool on every boot. Its slots are its own, so that no other VM, from
// this run of the tests or another, takes or frees one between the two
// boots. With a new prefix, it takes one new lease per run.
func TestMACReused(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := "C2VM_MAC_DIR=" + dir

	first := bootMAC(t, env)
	mac, err := net.ParseMAC(first)
	if err != nil {
		t.Fatalf("VM's MAC address %q: %v", first, err)
	}
	if mac[0]&0x02 == 0 || mac[0]&0x01 != 0 {
		t.Errorf("VM's MAC address %s isn't locally administered and unicast", mac)
	}
	// Otherwise, c2vm didn't use the test's slots.
	prefix, err := os.ReadFile(filepath.Join(dir, "prefix"))
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(string(prefix)) + ":00"; first != want {
		t.Errorf("VM's MAC address is %s, want %s, the first slot's", first, want)
	}

	if second := bootMAC(t, env); second != first {
		t.Errorf("second VM's MAC address is %s, want the first one's, %s", second, first)
	}
}

// TestMACConcurrent checks two VMs that run at the same time get
// different MAC addresses.
func TestMACConcurrent(t *testing.T) {
	t.Parallel()
	// The first VM prints its MAC and waits for stdin to close, so it
	// runs until the second one has stopped.
	cmd := bootCmd(t, "-i", image, "--", "sh", "-c", strings.Join(printMAC, " ")+" && cat >/dev/null")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	// The test's own pipe, rather than StdoutPipe, for a read deadline:
	// Virtualization.framework's VM process gets the write end too, and
	// may keep it open after c2vm has stopped, so neither bootCmd's
	// timeout nor WaitDelay would end a blocked read.
	stdout, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	cmd.Stdout = w
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err = cmd.Start()
	w.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := stdout.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		t.Fatal(err)
	}
	first, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		stdin.Close()
		cmd.Wait()
		t.Fatalf("reading the first VM's MAC address: %v\nc2vm's stderr:\n%s", err, stderr.String())
	}
	first = strings.TrimSpace(first)

	second := bootMAC(t)

	stdin.Close()
	if err := cmd.Wait(); err != nil {
		t.Errorf("first VM: %v\nc2vm's stderr:\n%s", err, stderr.String())
	}
	if first == second {
		t.Errorf("both VMs have the MAC address %s", first)
	}
}

func TestResolvedDigests(t *testing.T) {
	const kernel = "ghcr.io/fcuny/c2vm-kernel:7.3"
	d1, d2 := "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64)
	for _, tt := range []struct {
		name string
		log  string
	}{
		{"looked up", "c2vm: resolved " + kernel + " to " + d1 + "\n" +
			"c2vm: using cached /cache/kernels/sha256-aaaa\n" +
			"c2vm: resolved " + imageTag + " to " + d2 + "\r\n" +
			"c2vm: resolved short to sha256:abc\n"},
		// The image's lookup failed: c2vm used what the tag resolved to
		// before. The kernel's digest came from a record.
		{"fallback", "c2vm: resolved " + kernel + " to " + d1 + ", from the cache\n" +
			"c2vm: warning: failed to pull " + imageTag + ": TOOMANYREQUESTS: Rate exceeded\n" +
			"c2vm: using " + d2 + ", which " + imageTag + " resolved to on 2026-11-03: it may be out of date\n"},
	} {
		got := resolvedDigests(tt.log)
		if len(got) != 2 || got[kernel] != d1 || got[imageTag] != d2 {
			t.Errorf("%s: resolvedDigests = %v, want %s: %s and %s: %s", tt.name, got, kernel, d1, imageTag, d2)
		}
	}
}

func TestPinned(t *testing.T) {
	const digest = "sha256:aaaa"
	for ref, want := range map[string]string{
		"public.ecr.aws/docker/library/alpine:3.24": "public.ecr.aws/docker/library/alpine@sha256:aaaa",
		"ghcr.io/fcuny/c2vm-kernel@sha256:bbbb":     "ghcr.io/fcuny/c2vm-kernel@sha256:aaaa",
		"localhost:5000/c2vm-kernel:test":           "localhost:5000/c2vm-kernel@sha256:aaaa",
		"localhost:5000/c2vm-kernel":                "localhost:5000/c2vm-kernel@sha256:aaaa",
		"index.docker.io/library/alpine:latest":     "index.docker.io/library/alpine@sha256:aaaa",
	} {
		if got := pinned(ref, digest); got != want {
			t.Errorf("pinned(%s) = %s, want %s", ref, got, want)
		}
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

// TestStdout checks the command's stdout reaches c2vm's as it is: no
// terminal turns \n into \r\n, and nothing else is in it.
func TestStdout(t *testing.T) {
	t.Parallel()
	out, code := boot(t, bootCmd(t, image, "--", "printf", `a\nb`))
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if want := "a\nb"; out != want {
		t.Errorf("output %q, want %q", out, want)
	}
}

func TestStderr(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := bootOutputs(t, bootCmd(t, image, "--", "sh", "-c", "echo err >&2"))
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if stdout != "" {
		t.Errorf("stdout %q, want nothing", stdout)
	}
	if !hasLine(stderr, "err") {
		t.Errorf("stderr %q, want the command's line", stderr)
	}
}

// TestNotATerminal checks the command's outputs aren't terminals
// without -t, so programs don't turn on colors or progress bars.
func TestNotATerminal(t *testing.T) {
	t.Parallel()
	_, code := boot(t, bootCmd(t, image, "--", "sh", "-c", "[ -t 1 ] || [ -t 2 ]"))
	if code != 1 {
		t.Errorf("exit status %d, want 1", code)
	}
}

// TestDevStdout checks /dev/stdout and /dev/stderr can be opened, as
// images that log to them, like nginx's, expect.
func TestDevStdout(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := bootOutputs(t, bootCmd(t, image, "--", "sh", "-c", "echo out > /dev/stdout && echo err > /dev/stderr"))
	if code != 0 {
		t.Errorf("exit status %d, want 0\nstderr:\n%s", code, stderr)
	}
	if stdout != "out\n" {
		t.Errorf("stdout %q, want %q", stdout, "out\n")
	}
	if !hasLine(stderr, "err") {
		t.Errorf("stderr %q, want the command's line", stderr)
	}
}

// TestLargeOutput checks a large output arrives whole, though the
// command exits right after writing it.
func TestLargeOutput(t *testing.T) {
	t.Parallel()
	const size = 100 << 20
	script := fmt.Sprintf("head -c %d /dev/urandom > /tmp/data && sha256sum < /tmp/data >&2 && cat /tmp/data", size)
	cmd := bootCmd(t, image, "--", "sh", "-c", script)
	var stdout bytes.Buffer
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("c2vm boot: %v\nstderr:\n%s", err, stderr.String())
	}
	if stdout.Len() != size {
		t.Errorf("got %d bytes, want %d", stdout.Len(), size)
	}
	sum := sha256.Sum256(stdout.Bytes())
	if want := hex.EncodeToString(sum[:]) + "  -"; !hasLine(stderr.String(), want) {
		t.Errorf("the checksum of the output, %s, isn't the one computed in the guest:\n%s", want, stderr.String())
	}
}

// TestOutputBeforeExit checks what the command writes right before it
// exits isn't lost, though a process it left in the background still
// holds its outputs.
func TestOutputBeforeExit(t *testing.T) {
	t.Parallel()
	stdout, stderr, code := bootOutputs(t, bootCmd(t, image, "--", "sh", "-c", "sleep 600 & echo out; echo err >&2; exit 5"))
	if code != 5 {
		t.Errorf("exit status %d, want 5\nstderr:\n%s", code, stderr)
	}
	if stdout != "out\n" {
		t.Errorf("stdout %q, want %q", stdout, "out\n")
	}
	if !hasLine(stderr, "err") {
		t.Errorf("stderr %q, want the command's line", stderr)
	}
}

// TestStdoutClosed checks the command stops once c2vm's stdout goes
// away, as in c2vm boot ... | head -1: it gets SIGPIPE, as it would in
// a local pipeline.
func TestStdoutClosed(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := bootCmd(t, image, "--", "yes")
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = w, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()

	line := make([]byte, 2)
	if _, err := io.ReadFull(r, line); err != nil {
		t.Errorf("reading the first line: %v", err)
	}
	r.Close()
	_ = cmd.Wait()
	if code := cmd.ProcessState.ExitCode(); code != 128+int(syscall.SIGPIPE) {
		t.Errorf("exit status %d, want %d, from SIGPIPE\nstderr:\n%s", code, 128+int(syscall.SIGPIPE), stderr.String())
	}
	if string(line) != "y\n" {
		t.Errorf("first line %q, want %q", line, "y\n")
	}
	// The reader going away isn't an error.
	if strings.Contains(stderr.String(), "passing the command's output") {
		t.Errorf("init reported an error passing the output:\n%s", stderr.String())
	}
}

// TestBackgroundWriter checks the VM stops once the command exits,
// though a process it left in the background writes to its stdout
// faster than c2vm can pass it on.
func TestBackgroundWriter(t *testing.T) {
	t.Parallel()
	cmd := bootCmd(t, image, "--", "sh", "-c", "yes & sleep 1; exit 6")
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	_ = cmd.Run()
	if code := cmd.ProcessState.ExitCode(); code != 6 {
		t.Errorf("exit status %d, want 6\nstderr:\n%s", code, stderr.String())
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

// writeRoot writes count MiB to /, the overlay, whose writable layer
// holds it, then prints the size of / in KiB.
const writeRoot = `dd if=/dev/zero of=/big bs=1M count=%d 2>&1 && df -k / | awk 'NR == 2 { print $2 }'`

// TestNoScratch checks what -scratch is for: without it, the writable
// layer is a tmpfs, which can't take more than half of the VM's memory.
func TestNoScratch(t *testing.T) {
	t.Parallel()
	out, code := boot(t, bootCmd(t, "-memory", "512", image, "--", "sh", "-c", fmt.Sprintf(writeRoot, 600)))
	if code == 0 {
		t.Error("exit status 0, want writing 600 MiB in a 512 MiB VM to fail")
	}
	if !strings.Contains(out, "No space left on device") {
		t.Errorf("output %q, want ENOSPC", out)
	}
}

func TestScratch(t *testing.T) {
	t.Parallel()
	out, code := boot(t, scratchCmd(t, "-memory", "512", "-scratch", "1G", image, "--", "sh", "-c", fmt.Sprintf(writeRoot, 600)))
	if code != 0 {
		t.Fatalf("exit status %d, want 0: %s", code, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	size, err := strconv.Atoi(lines[len(lines)-1])
	if err != nil {
		t.Fatalf("no size for / in %q", out)
	}
	// The disk, less what ext4 keeps for itself.
	if size < 900<<10 || size > 1<<20 {
		t.Errorf("/ is %d KiB, want about 1 GiB", size)
	}
}

// TestScratchXattr checks that the overlay keeps user extended
// attributes on the scratch disk, as ostree needs in bare-user mode. A
// tmpfs has them too on 6.6 and later kernels, so this checks the ext4
// layer, not what sets it apart.
func TestScratchXattr(t *testing.T) {
	t.Parallel()
	out, code := boot(t, scratchCmd(t, "-scratch", "64M", "-v", guestTools+":/tools:ro", image, "--",
		"sh", "-c", "touch /file && /tools/xattr /file user.c2vm 'from the guest'"))
	if code != 0 {
		t.Errorf("exit status %d, want 0", code)
	}
	if want := "from the guest\n"; out != want {
		t.Errorf("output %q, want %q", out, want)
	}
}

// TestScratchRemoved checks that the scratch disk's file is gone once the
// VM runs, so that it can't outlive c2vm, and that nothing is left once
// it stops.
func TestScratchRemoved(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	cmd := scratchCmd(t, "-scratch", "64M", image, "--", "sh", "-c", "echo started && sleep 5")
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("c2vm's stderr:\n%s", &stderr)
		}
	})
	// Cleanups run last first: when the test stops early, c2vm is
	// stopped, and done writing to stderr, before the one above reads it.
	waited := false
	t.Cleanup(func() {
		if !waited {
			cmd.Process.Signal(syscall.SIGTERM)
			cmd.Wait()
		}
	})

	if line, err := bufio.NewReader(stdout).ReadString('\n'); line != "started\n" {
		t.Fatalf("read %q (%v), want the command to start", line, err)
	}
	if runDirs, _ := filepath.Glob(filepath.Join(tmp, "c2vm*")); len(runDirs) != 1 {
		t.Errorf("c2vm's directories in TMPDIR: %q, want one", runDirs)
	}
	if left, _ := filepath.Glob(filepath.Join(tmp, "c2vm*", "scratch.img")); len(left) != 0 {
		t.Errorf("%q still there while the VM runs", left)
	}

	io.Copy(io.Discard, stdout)
	waited = true
	if err := cmd.Wait(); err != nil {
		t.Errorf("c2vm: %v", err)
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("%d entries left in TMPDIR once c2vm exited", len(entries))
	}
}

// TestScratchKernelFile checks that -scratch with a kernel file, which
// has no mkfs.ext4, fails before booting, saying why.
func TestScratchKernelFile(t *testing.T) {
	t.Parallel()
	kernel := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(kernel, []byte("not a kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, code := bootOutputs(t, bootCmd(t, "-kernel", kernel, "-scratch", "64M", image, "--", "true"))
	if code == 0 {
		t.Error("exit status 0, want c2vm to refuse")
	}
	if !strings.Contains(stderr, "without the mkfs.ext4 a scratch disk needs") {
		t.Errorf("stderr %q, want it to say mkfs.ext4 is missing", stderr)
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
		// done, so it was sent SIGTERM, or SIGQUIT past its timeout, and
		// is killed if it doesn't stop in time. Wait for it, not to leave it behind.
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

// waitFor waits for s to show up on the terminal. It fails as soon as
// the terminal's output ends without it: c2vm exited, e.g. as the image
// couldn't be pulled, or was stopped as it ran past its timeout, which
// bootCmd reports.
func (term *terminal) waitFor(s string) {
	term.t.Helper()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	// bootCmd's context, which started first, runs out before this: it's
	// in case c2vm doesn't stop then.
	deadline := time.After(timeout + time.Minute)
	for !strings.Contains(term.output(), s) {
		select {
		case <-term.done:
			// The last read may have brought s.
			if strings.Contains(term.output(), s) {
				return
			}
			term.t.Fatalf("the terminal's output ended before %q showed up", s)
		case <-deadline:
			term.t.Fatalf("%q never showed up on the terminal", s)
		case <-tick.C:
		}
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
