//go:build linux

// Command c2vm-init is the init process of VMs built by c2vm. It runs
// from an initramfs: it mounts the image read-only with a writable
// layer on top, switches to it, sets up the minimum a container
// expects, runs the image's command as the image's user, and stops the
// VM when the command exits, after reporting its exit status to the
// host.
package main

import (
	"errors"
	"fmt"
	"io"
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

// rootDevice is the image: the VM's first drive. A scratch disk, when
// there's one, is the second (see guest.ScratchDisk).
const rootDevice = "/dev/vda"

func main() {
	// Read the configuration before switching to the image: it lives in
	// the initramfs.
	config, err := guest.ReadConfig(guest.ConfigPath)
	if err != nil {
		logf("%v", err)
		shutdown(guest.ShutdownReboot)
	}
	if config.Protocol != guest.Protocol {
		// c2vm checks the init it boots, unless it's older than that
		// check.
		logf("this c2vm-init speaks %q, and c2vm %q: boot the c2vm-init built with c2vm", guest.Protocol, config.Protocol)
		shutdown(config.Shutdown)
	}

	// The connections for the command's outputs stay open until the host
	// has acknowledged the status: closing them could lose what's still
	// on its way.
	var conns []*os.File
	status, err := run(config, &conns)
	if err != nil {
		logf("%v", err)
		status.Code = 1
	}
	logf("exiting with status %d, shutting down", status.Code)
	if err := reportStatus(status); err != nil {
		logf("reporting the exit status: %v", err)
	}
	for _, conn := range conns {
		conn.Close()
	}
	shutdown(config.Shutdown)
}

// reportStatus sends the command's exit status to the host over vsock.
// It then waits for the host to close the connection, which it does once
// it has written the command's output, so that's written and the status
// read before the VM stops. That has no time limit: if the host's stdout
// is blocked, so is init, as a process writing to a full pipe would be.
func reportStatus(status guest.Status) error {
	fd, err := dialHost(guest.StatusPort)
	if err != nil {
		return err
	}
	defer unix.Close(fd)

	if _, err := unix.Write(fd, guest.EncodeStatus(status)); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	var buf [1]byte
	for {
		_, err := unix.Read(fd, buf[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return fmt.Errorf("waiting for the host: %w", err)
		}
		return nil
	}
}

// run runs the command, and returns its exit status. Without -t, it
// adds the connections for the command's outputs to conns, and the
// status says how many bytes it sent on them, even if it fails.
func run(config guest.Config, conns *[]*os.File) (status guest.Status, err error) {
	if err := switchRoot(config.Scratch); err != nil {
		return status, err
	}

	mountFilesystems()
	if err := mountShares(config.Mounts); err != nil {
		return status, err
	}
	linkDevices()
	logAddresses()

	if err := guest.SetupEtc("/"); err != nil {
		return status, fmt.Errorf("setting up /etc: %w", err)
	}

	cred, err := guest.LookupUser("/", config.User)
	if err != nil {
		return status, err
	}

	env := guest.Environ(config.Env, cred.Home)
	path, _ := guest.Getenv(env, "PATH")
	argv0, err := guest.LookPath(config.Args[0], path)
	if err != nil {
		return status, err
	}

	dir := config.WorkingDir
	if dir == "" {
		dir = "/"
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return status, err
	}

	cmd := exec.Command(argv0, config.Args[1:]...)
	cmd.Args[0] = config.Args[0]
	cmd.Env = env
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{
			Uid:    cred.UID,
			Gid:    cred.GID,
			Groups: cred.Groups,
		},
	}
	// The command's ends of its outputs, which init closes once the
	// command has them.
	var outputs []*os.File
	if config.TTY {
		console, err := os.OpenFile(guest.CommandConsole, os.O_RDWR|unix.O_NOCTTY, 0)
		if err != nil {
			return status, fmt.Errorf("opening the command's console: %w", err)
		}
		defer console.Close()
		setSize(int(console.Fd()), config.Rows, config.Columns)
		go followResizes(console)
		// The command leads a new session, with its console as its
		// controlling terminal: the console's line discipline then
		// turns ^C into SIGINT, ^Z into SIGTSTP, and so on.
		cmd.Stdin, cmd.Stdout, cmd.Stderr = console, console, console
		cmd.SysProcAttr.Setsid = true
		cmd.SysProcAttr.Setctty = true
		cmd.SysProcAttr.Ctty = 0
	} else {
		// stdout and stderr go to the host's separately, byte for byte.
		for _, o := range []struct {
			port uint32
			dst  *io.Writer
			sent *int64
		}{
			{guest.StdoutPort, &cmd.Stdout, &status.Stdout},
			{guest.StderrPort, &cmd.Stderr, &status.Stderr},
		} {
			conn, err := dialOutput(o.port)
			if err != nil {
				return status, err
			}
			*conns = append(*conns, conn)
			output, w, err := guest.NewOutput(conn)
			if err != nil {
				return status, err
			}
			// Once the command has exited, or failed to start, for the
			// status to say how much of its output there is.
			defer func() {
				sent, err := output.Finish()
				if err != nil {
					logf("passing the command's output: %v", err)
				}
				*o.sent = sent
			}()
			defer w.Close()
			*o.dst = w
			outputs = append(outputs, w)
		}
		// Without -i, the command reads /dev/null, as with docker run.
		if config.Stdin {
			stdin, err := openStdin()
			if err != nil {
				return status, err
			}
			defer stdin.Close()
			cmd.Stdin = stdin
		}
	}

	// Forward the signals we get to the command, as a container runtime
	// would. Register before starting it so none are missed.
	signals := make(chan os.Signal, 16)
	signal.Notify(signals, unix.SIGTERM, unix.SIGINT, unix.SIGHUP, unix.SIGQUIT, unix.SIGUSR1, unix.SIGUSR2)

	if err := cmd.Start(); err != nil {
		return status, err
	}
	// Deferred after the Finishes, so it runs before them on every path:
	// they read the pipes to their end, which comes once every process
	// holding them is gone.
	defer killLeftovers()
	// The pipes end once the command, and whatever it leaves behind, have
	// closed their ends.
	for _, w := range outputs {
		w.Close()
	}

	go func() {
		for sig := range signals {
			_ = cmd.Process.Signal(sig)
		}
	}()

	status.Code, err = reap(cmd.Process.Pid)
	return status, err
}

// dialHost connects to port on the host, over vsock.
func dialHost(port uint32) (int, error) {
	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := unix.Connect(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_HOST, Port: port}); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("connect to the host's port %d: %w", port, err)
	}
	return fd, nil
}

// openStdin connects to the host for the command's stdin, which ends
// when the host's does.
func openStdin() (*os.File, error) {
	fd, err := dialHost(guest.StdinPort)
	if err != nil {
		return nil, fmt.Errorf("connecting stdin: %w", err)
	}
	return os.NewFile(uintptr(fd), "stdin"), nil
}

// dialOutput connects to port on the host, for one of the command's
// outputs.
func dialOutput(port uint32) (*os.File, error) {
	fd, err := dialHost(port)
	if err != nil {
		return nil, fmt.Errorf("connecting the command's output: %w", err)
	}
	return os.NewFile(uintptr(fd), "vsock"), nil
}

// followResizes sets the terminal's size whenever the host's changes.
// The kernel then sends SIGWINCH to the command.
func followResizes(tty *os.File) {
	fd, err := dialHost(guest.ResizePort)
	if err != nil {
		logf("following the terminal's size: %v", err)
		return
	}
	conn := os.NewFile(uintptr(fd), "vsock")
	defer conn.Close()

	ttyFD := int(tty.Fd())
	err = guest.ReadSizes(conn, func(rows, columns uint16) {
		setSize(ttyFD, rows, columns)
	})
	if err != nil {
		logf("following the terminal's size: %v", err)
	}
}

func setSize(fd int, rows, columns uint16) {
	if rows == 0 || columns == 0 {
		return
	}
	size := &unix.Winsize{Row: rows, Col: columns}
	if err := unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, size); err != nil {
		logf("setting the terminal's size: %v", err)
	}
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

// killLeftovers kills the processes the command left behind, and reaps
// them, as the kernel does in a container once its main process exits:
// SIGKILL, with no grace period. Once it returns, nothing writes to the
// command's outputs or to the volumes anymore.
//
// It kills again before each wait, so a process forked as the first
// SIGKILL went out doesn't survive. It stops on ECHILD, not on kill's
// ESRCH: from PID 1, kill(-1) also reaches kernel threads, which ignore
// the signal, so it never fails. A process in uninterruptible sleep, e.g.
// writing to a volume the host doesn't answer for, dies only once it
// leaves it, and init waits for it.
func killLeftovers() {
	for {
		if err := unix.Kill(-1, unix.SIGKILL); err != nil && !errors.Is(err, unix.ESRCH) {
			logf("killing the processes left behind: %v", err)
		}
		_, err := unix.Wait4(-1, nil, 0, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.ECHILD) {
			return
		}
		if err != nil {
			logf("reaping the processes left behind: %v", err)
			return
		}
	}
}

// switchRoot mounts the image read-only, with a writable layer on top,
// and makes that the root filesystem. The writable layer is a tmpfs, or
// the scratch disk when there's one.
func switchRoot(scratch *guest.ScratchDisk) error {
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
	if err := mountWritable(scratch); err != nil {
		return err
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

// mountWritable mounts what holds the overlay's writable layer on
// guest.WritableDir. Without a scratch disk, it's a tmpfs, whose size
// the kernel caps at half the VM's memory. With one, it formats the disk
// and mounts it.
func mountWritable(scratch *guest.ScratchDisk) error {
	if scratch == nil {
		if err := unix.Mount("tmpfs", guest.WritableDir, "tmpfs", 0, "mode=0755"); err != nil {
			return fmt.Errorf("mount %s: %w", guest.WritableDir, err)
		}
		return nil
	}

	if err := waitFor(scratch.Device, 5*time.Second); err != nil {
		return err
	}
	// The disk is thrown away with the VM, which shapes the options:
	//   - no journal: there's nothing to recover after a crash;
	//   - no blocks reserved for root (-m 0): the command may run as
	//     another user, and would lose 5% of the disk;
	//   - the inode tables aren't zeroed, neither by mke2fs
	//     (lazy_itable_init) nor in the background once mounted
	//     (noinit_itable, below). ext4 only reads the part of each
	//     group's table that's in use; zeroing the rest helps e2fsck
	//     after corruption, and would write it all out to the host's
	//     sparse file.
	// An empty environment: mke2fs uses its built-in defaults, as there's
	// no /etc/mke2fs.conf in the initramfs.
	mkfs := exec.Command(guest.MkfsPath, "-F", "-q", "-t", scratch.FSType,
		"-O", "^has_journal", "-m", "0", "-E", "lazy_itable_init=1,nodiscard", scratch.Device)
	mkfs.Env = []string{}
	if out, err := mkfs.CombinedOutput(); err != nil {
		return fmt.Errorf("formatting %s: %w\n%s", scratch.Device, err, out)
	}
	if err := unix.Mount(scratch.Device, guest.WritableDir, scratch.FSType, unix.MS_NOATIME, "noinit_itable"); err != nil {
		return fmt.Errorf("mount %s on %s: %w", scratch.Device, guest.WritableDir, err)
	}
	return nil
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

// mountShares mounts the host's directories, with virtiofs. Unlike
// mountFilesystems, a failure is fatal: the command expects them.
func mountShares(mounts []guest.Mount) error {
	for _, m := range mounts {
		if err := os.MkdirAll(m.Target, 0755); err != nil {
			return fmt.Errorf("mount %s: %w", m.Target, err)
		}
		var flags uintptr
		if m.ReadOnly {
			flags |= unix.MS_RDONLY
		}
		if err := unix.Mount(m.Tag, m.Target, "virtiofs", flags, ""); err != nil {
			return fmt.Errorf("mount %s: %w", m.Target, err)
		}
	}
	return nil
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
	// If that failed, returning from PID 1 panics the kernel: the host
	// sees the panic on the kernel's console and stops the VM.
	os.Exit(1)
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "c2vm-init: "+format+"\n", args...)
}
