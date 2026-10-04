//go:build linux

// Command c2vm-init is the init process of VMs built by c2vm. It runs
// from an initramfs: it mounts the image read-only with a writable
// layer on top, switches to it, sets up the minimum a container
// expects, runs the image's command as the image's user, and stops the
// VM when the command exits.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"fcuny.net/c2vm/internal/guest"
)

// rootDevice is the image: the VM's first, and only, drive.
const rootDevice = "/dev/vda"

func main() {
	// Read the configuration before switching to the image: it lives in
	// the initramfs.
	config, err := guest.ReadConfig(guest.ConfigPath)
	if err != nil {
		logf("%v", err)
		shutdown(guest.ShutdownReboot)
	}

	code, err := run(config)
	if err != nil {
		logf("%v", err)
		code = 1
	}
	logf("exiting with status %d, shutting down", code)
	shutdown(config.Shutdown)
}

func run(config guest.Config) (int, error) {
	if err := switchRoot(); err != nil {
		return 0, err
	}

	mountFilesystems()
	linkDevices()
	logAddresses()

	if err := guest.SetupEtc("/"); err != nil {
		return 0, fmt.Errorf("setting up /etc: %w", err)
	}

	cred, err := guest.LookupUser("/", config.User)
	if err != nil {
		return 0, err
	}

	env := guest.Environ(config.Env, cred.Home)
	path, _ := guest.Getenv(env, "PATH")
	argv0, err := guest.LookPath(config.Args[0], path)
	if err != nil {
		return 0, err
	}

	dir := config.WorkingDir
	if dir == "" {
		dir = "/"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, err
	}

	cmd := exec.Command(argv0, config.Args[1:]...)
	cmd.Args[0] = config.Args[0]
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:    cred.UID,
			Gid:    cred.GID,
			Groups: cred.Groups,
		},
	}

	// Forward the signals we get to the command, as a container runtime
	// would. Register before starting it so none are missed.
	signals := make(chan os.Signal, 16)
	signal.Notify(signals, unix.SIGTERM, unix.SIGINT, unix.SIGHUP, unix.SIGQUIT, unix.SIGUSR1, unix.SIGUSR2)

	if err := cmd.Start(); err != nil {
		return 0, err
	}

	go func() {
		for sig := range signals {
			_ = cmd.Process.Signal(sig)
		}
	}()

	return reap(cmd.Process.Pid)
}

// reap waits for every child, as PID 1 has to so orphans don't stay
// zombies, and returns the exit status of pid once it exits.
func reap(pid int) (int, error) {
	for {
		var status unix.WaitStatus
		wpid, err := unix.Wait4(-1, &status, 0, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("wait: %w", err)
		}
		if wpid != pid {
			continue
		}
		if status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return status.ExitStatus(), nil
	}
}

// switchRoot mounts the image read-only, with a tmpfs on top to hold
// writes, and makes that the root filesystem.
func switchRoot() error {
	// With an initramfs, the kernel doesn't mount devtmpfs for us.
	if err := unix.Mount("devtmpfs", "/dev", "devtmpfs", unix.MS_NOSUID, "mode=0755"); err != nil {
		return fmt.Errorf("mount /dev: %w", err)
	}

	if err := waitFor(rootDevice, 5*time.Second); err != nil {
		return err
	}

	if err := unix.Mount(rootDevice, guest.LowerDir, "ext4", unix.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount %s: %w", rootDevice, err)
	}
	if err := unix.Mount("tmpfs", guest.WritableDir, "tmpfs", 0, "mode=0755"); err != nil {
		return fmt.Errorf("mount %s: %w", guest.WritableDir, err)
	}

	upper := filepath.Join(guest.WritableDir, "upper")
	work := filepath.Join(guest.WritableDir, "work")
	for _, dir := range []string{upper, work} {
		if err := os.Mkdir(dir, 0755); err != nil {
			return err
		}
	}
	opts := fmt.Sprintf("lowerdir=%s,upperdir=%s,workdir=%s", guest.LowerDir, upper, work)
	if err := unix.Mount("overlay", guest.NewRootDir, "overlay", 0, opts); err != nil {
		return fmt.Errorf("mount overlay: %w", err)
	}

	newDev := filepath.Join(guest.NewRootDir, "dev")
	if err := os.MkdirAll(newDev, 0755); err != nil {
		return err
	}
	if err := unix.Mount("/dev", newDev, "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move /dev: %w", err)
	}

	// The initramfs can't be unmounted or pivoted away from, so move the
	// new root over it and chroot, as busybox's switch_root does.
	if err := unix.Chdir(guest.NewRootDir); err != nil {
		return err
	}
	if err := unix.Mount(".", "/", "", unix.MS_MOVE, ""); err != nil {
		return fmt.Errorf("move the new root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return err
	}
	return unix.Chdir("/")
}

// waitFor waits for a device node to appear, in case its driver probes
// after init starts.
func waitFor(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		_, err := os.Stat(path)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waiting for %s: %w", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// mountFilesystems mounts what the kernel doesn't. Failures are logged
// but not fatal, the command may well run without them.
func mountFilesystems() {
	for _, m := range []struct {
		source, target, fstype string
		flags                  uintptr
		data                   string
	}{
		{"proc", "/proc", "proc", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"sysfs", "/sys", "sysfs", unix.MS_NOSUID | unix.MS_NODEV | unix.MS_NOEXEC, ""},
		{"devpts", "/dev/pts", "devpts", unix.MS_NOSUID | unix.MS_NOEXEC, "newinstance,ptmxmode=0666,mode=0620"},
		{"shm", "/dev/shm", "tmpfs", unix.MS_NOSUID | unix.MS_NODEV, "mode=1777"},
	} {
		if err := os.MkdirAll(m.target, 0755); err != nil {
			logf("mkdir %s: %v", m.target, err)
			continue
		}
		if err := unix.Mount(m.source, m.target, m.fstype, m.flags, m.data); err != nil {
			logf("mount %s: %v", m.target, err)
		}
	}
}

// linkDevices creates the links container runtimes add to /dev, which
// devtmpfs doesn't have. Images rely on them, e.g. nginx's logs are
// symlinks to /dev/stdout and /dev/stderr.
func linkDevices() {
	for link, target := range map[string]string{
		"/dev/fd":     "/proc/self/fd",
		"/dev/stdin":  "/proc/self/fd/0",
		"/dev/stdout": "/proc/self/fd/1",
		"/dev/stderr": "/proc/self/fd/2",
		"/dev/core":   "/proc/kcore",
		// devpts is mounted with newinstance, so the multiplexer to use
		// is its own, not devtmpfs' /dev/ptmx.
		"/dev/ptmx": "pts/ptmx",
	} {
		if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
			logf("remove %s: %v", link, err)
			continue
		}
		if err := os.Symlink(target, link); err != nil {
			logf("symlink %s: %v", link, err)
		}
	}
}

// logAddresses reports the VM's addresses, to know where to reach it.
func logAddresses() {
	ifaces, err := net.Interfaces()
	if err != nil {
		logf("listing network interfaces: %v", err)
		return
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ip, ok := addr.(*net.IPNet); ok && ip.IP.To4() != nil {
				logf("%s: %s", iface.Name, ip)
			}
		}
	}
}

// shutdown stops the VM, by rebooting or powering off the guest
// depending on what makes the hypervisor stop it.
func shutdown(how string) {
	unix.Sync()
	cmd := unix.LINUX_REBOOT_CMD_RESTART
	if how == guest.ShutdownPowerOff {
		cmd = unix.LINUX_REBOOT_CMD_POWER_OFF
	}
	if err := unix.Reboot(cmd); err != nil {
		logf("%s: %v", how, err)
	}
	// If that failed, returning from PID 1 panics the kernel, which with
	// panic=1 also stops the VM.
	os.Exit(1)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "c2vm-init: "+format+"\n", args...)
}
