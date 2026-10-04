// Command c2vm boots container images as microVMs.
//
//	c2vm save [flags] <image>
//	c2vm boot [flags] <image> [-- command [args...]]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/renameio/v2"

	"fcuny.net/c2vm/internal/cache"
	"fcuny.net/c2vm/internal/guest"
	"fcuny.net/c2vm/internal/image"
	"fcuny.net/c2vm/internal/initramfs"
	"fcuny.net/c2vm/internal/vm"
)

const usage = `usage: c2vm <command> [flags] <image>

Commands:
  save  pull an image and convert it for booting
  boot  boot an image as a VM

Run c2vm <command> -h for the command's flags.
`

func main() {
	log.SetFlags(0)
	log.SetPrefix("c2vm: ")

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "save":
		err = runSave(args)
	case "boot":
		err = runBoot(args)
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "c2vm: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}

	if errors.Is(err, flag.ErrHelp) {
		return
	}
	var exit exitError
	if errors.As(err, &exit) {
		os.Exit(int(exit))
	}
	if err != nil {
		log.Fatal(err)
	}
}

// exitError is the non-zero exit status of the command run in a VM,
// which c2vm exits with.
type exitError int

func (e exitError) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// commonOptions are the options of every command that pulls an image.
type commonOptions struct {
	image    string
	platform v1.Platform
	cacheDir string
}

func (c *commonOptions) register(fs *flag.FlagSet) func() error {
	var platform string
	fs.StringVar(&platform, "platform", "linux/"+runtime.GOARCH, "Platform of the image to pull")
	fs.StringVar(&c.cacheDir, "cache-dir", "", "Directory to cache images in (default: the user's cache directory)")

	return func() error {
		p, err := v1.ParsePlatform(platform)
		if err != nil {
			return fmt.Errorf("invalid -platform: %w", err)
		}
		if p.OS != "linux" {
			return fmt.Errorf("invalid -platform %q: only linux images can be booted", platform)
		}
		c.platform = *p

		if c.cacheDir == "" {
			dir, err := cache.DefaultDir()
			if err != nil {
				return err
			}
			c.cacheDir = dir
		}
		return nil
	}
}

// parse parses args, which hold one image name, with flags before or
// after it.
func parse(fs *flag.FlagSet, args []string) (string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}

	switch len(positional) {
	case 0:
		return "", errors.New("an image is required")
	case 1:
		return positional[0], nil
	default:
		return "", fmt.Errorf("expected one image, got %s", strings.Join(positional, " "))
	}
}

// splitCommand splits args at the first --, into c2vm's flags and image,
// and the command to run in the VM.
func splitCommand(args []string) (before, command []string, found bool) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:], true
		}
	}
	return args, nil, false
}

type saveOptions struct {
	commonOptions
	out string
}

func parseSave(args []string) (saveOptions, error) {
	var opts saveOptions
	fs := flag.NewFlagSet("c2vm save", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: c2vm save [flags] <image>\n\nPulls an image and converts it to an ext4 image, stored in the cache. Prints its path.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	finish := opts.register(fs)
	fs.StringVar(&opts.out, "o", "", "Also copy the ext4 image to this path")

	image, err := parse(fs, args)
	if err != nil {
		return saveOptions{}, err
	}
	opts.image = image
	return opts, finish()
}

func runSave(args []string) error {
	opts, err := parseSave(args)
	if err != nil {
		return err
	}

	_, path, err := pullImage(context.Background(), opts.commonOptions)
	if err != nil {
		return err
	}

	if opts.out != "" {
		if err := copyFile(path, opts.out); err != nil {
			return err
		}
		path = opts.out
	}
	fmt.Println(path)
	return nil
}

type bootOptions struct {
	commonOptions
	kernel     string
	initBinary string
	cpus       int64
	memoryMiB  int64
	backend    func() (vm.Backend, error)
	// command replaces the image's command when set.
	command []string
	// env is added to the image's environment, as KEY=value pairs.
	env envFlag
}

// envFlag is a repeatable flag of environment variables. KEY=value sets
// KEY; KEY alone copies KEY from c2vm's environment, and is ignored when
// it isn't set there, as with docker run.
type envFlag []string

func (e *envFlag) String() string { return strings.Join(*e, ",") }

func (e *envFlag) Set(value string) error {
	key, _, hasValue := strings.Cut(value, "=")
	if key == "" {
		return fmt.Errorf("invalid variable %q: the name is empty", value)
	}
	if !hasValue {
		v, ok := os.LookupEnv(key)
		if !ok {
			return nil
		}
		value = key + "=" + v
	}
	*e = append(*e, value)
	return nil
}

func parseBoot(args []string) (bootOptions, error) {
	var opts bootOptions
	fs := flag.NewFlagSet("c2vm boot", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: c2vm boot [flags] <image> [-- command [args...]]\n\nBoots an image as a VM, with its console on the terminal. A command after -- replaces the image's command, and is passed to its entrypoint, if it has one.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	finish := opts.register(fs)
	fs.StringVar(&opts.kernel, "kernel", defaultKernel, "Kernel to boot: a file, or an image with the kernel in "+kernelPath)
	fs.StringVar(&opts.initBinary, "init", "", "Path to the c2vm-init binary (default: c2vm-init next to c2vm)")
	fs.Int64Var(&opts.cpus, "cpus", 1, "Number of vCPUs")
	fs.Int64Var(&opts.memoryMiB, "memory", 512, "Memory for the VM, in MiB")
	fs.Var(&opts.env, "e", "Set an environment variable, as KEY=value, or KEY to copy it from c2vm's environment (repeatable)")
	opts.backend = backendFlags(fs)

	args, command, hasCommand := splitCommand(args)
	if hasCommand && len(command) == 0 {
		return bootOptions{}, errors.New("expected a command after --")
	}
	opts.command = command

	image, err := parse(fs, args)
	if err != nil {
		return bootOptions{}, err
	}
	opts.image = image

	if opts.cpus < 1 {
		return bootOptions{}, errors.New("-cpus must be at least 1")
	}
	if opts.memoryMiB < 1 {
		return bootOptions{}, errors.New("-memory must be at least 1")
	}
	return opts, finish()
}

func runBoot(args []string) error {
	opts, err := parseBoot(args)
	if err != nil {
		return err
	}

	backend, err := opts.backend()
	if err != nil {
		return err
	}

	initBinary, err := findInit(opts.initBinary)
	if err != nil {
		return err
	}

	ctx := context.Background()

	kernel, err := resolveKernel(ctx, opts.kernel, opts.cacheDir)
	if err != nil {
		return err
	}

	img, imagePath, err := pullImage(ctx, opts.commonOptions)
	if err != nil {
		return err
	}

	imageConfig, err := img.Config()
	if err != nil {
		return err
	}
	// As with docker run, the command replaces the image's command, but
	// not its entrypoint.
	if opts.command != nil {
		imageConfig.Cmd = opts.command
	}
	// The last value of a variable wins, so these override the image's.
	imageConfig.Env = append(imageConfig.Env, opts.env...)
	config, err := guest.FromImage(imageConfig)
	if err != nil {
		return err
	}
	config.Shutdown = backend.Shutdown()

	runDir, err := os.MkdirTemp("", "c2vm")
	if err != nil {
		return err
	}
	defer os.RemoveAll(runDir)

	initrd := filepath.Join(runDir, "initrd.cpio")
	if err := writeInitramfs(initrd, initBinary, config); err != nil {
		return fmt.Errorf("failed to build the initramfs: %w", err)
	}

	code, err := backend.Run(ctx, vm.Spec{
		Kernel:    kernel,
		Initrd:    initrd,
		Image:     imagePath,
		CPUs:      opts.cpus,
		MemoryMiB: opts.memoryMiB,
	})
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError(code)
	}
	return nil
}

// pullImage resolves the image and returns it with the path of its ext4
// image in the cache, which is only built if it isn't there yet.
func pullImage(ctx context.Context, opts commonOptions) (*image.Image, string, error) {
	img, err := image.Pull(ctx, opts.image, opts.platform)
	if err != nil {
		return nil, "", err
	}

	digest, err := img.Digest()
	if err != nil {
		return nil, "", err
	}

	path, cached, err := cache.New(opts.cacheDir).Get("images", digest, img.WriteExt4)
	if err != nil {
		return nil, "", err
	}
	if cached {
		log.Printf("using cached %s\n", path)
	} else {
		log.Printf("wrote %s\n", path)
	}
	return img, path, nil
}

// copyFile copies src to dst, atomically.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	f, err := renameio.NewPendingFile(dst, renameio.WithPermissions(0o644))
	if err != nil {
		return err
	}
	defer f.Cleanup()

	if _, err := io.Copy(f, in); err != nil {
		return err
	}
	return f.CloseAtomicallyReplace()
}

// findInit returns the path to the c2vm-init binary: path if set,
// otherwise c2vm-init in the same directory as c2vm.
func findInit(path string) (string, error) {
	if path == "" {
		self, err := os.Executable()
		if err != nil {
			return "", err
		}
		path = filepath.Join(filepath.Dir(self), "c2vm-init")
	}

	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("c2vm-init not found (build it with make, or set -init): %w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	return path, nil
}

func writeInitramfs(path, initBinary string, config guest.Config) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := initramfs.Write(f, initBinary, config); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
