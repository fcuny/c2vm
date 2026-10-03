//go:build linux

// Command c2vm-init is the init process of VMs built by c2vm. It sets
// up the minimum a container expects, runs the image's command as the
// image's user, and powers the VM off when the command exits.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"

	"fcuny.net/containerd-to-vm/internal/guest"
)

func main() {
	code, err := run()
	if err != nil {
		logf("%v", err)
		code = 1
	}
	logf("exiting with status %d, shutting down", code)
	shutdown()
}

func run() (int, error) {
	mountFilesystems()

	config, err := guest.ReadConfig(guest.ConfigPath)
	if err != nil {
		return 0, err
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

// shutdown stops the VM. With reboot=k on the kernel command line,
// firecracker exits when the guest reboots.
func shutdown() {
	unix.Sync()
	if err := unix.Reboot(unix.LINUX_REBOOT_CMD_RESTART); err != nil {
		logf("reboot: %v", err)
	}
	// If the reboot failed, returning from PID 1 panics the kernel,
	// which with panic=1 also stops the VM.
	os.Exit(1)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "c2vm-init: "+format+"\n", args...)
}
