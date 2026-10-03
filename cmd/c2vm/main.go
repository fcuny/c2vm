package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/platforms"
	"github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/google/renameio/v2"
	"github.com/moby/go-archive"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	containerdSock   = "/run/containerd/containerd.sock"
	defaultNamespace = "c2vm"
)

var (
	firecrackerSock = filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "firecracker.sock")
	platform        = platforms.Only(ocispec.Platform{
		OS:           "linux",
		Architecture: "amd64",
	})
)

func main() {
	var (
		containerName     = flag.String("container", "", "Name of the container")
		outFile           = flag.String("out", "container.img", "Path to store the image")
		kernel            = flag.String("kernel", "", "Path to the linux kernel image")
		firecrackerBinary = flag.String("firecracker-binary", "", "Path to the firecracker binary")
		metricsFifo       = flag.String("metrics-fifo", "", "FIFO to the firecracker metrics")
	)

	flag.Parse()

	if *containerName == "" {
		log.Fatal("a container is required")
	}

	if *kernel == "" {
		log.Fatal("a linux kernel is required")
	}

	if *firecrackerBinary == "" {
		log.Fatal("the path to the firecracker binary is required")
	}

	if err := run(*containerName, *outFile, *kernel, *firecrackerBinary, *metricsFifo); err != nil {
		log.Fatal(err)
	}
}

func run(containerName, outFile, kernel, firecrackerBinary, metricsFifo string) error {
	c, err := client.New(containerdSock)
	if err != nil {
		return fmt.Errorf("failed to create a client for containerd: %w", err)
	}
	defer c.Close()

	ctx := namespaces.WithNamespace(context.Background(), defaultNamespace)
	ctx, done, err := c.WithLease(ctx)
	if err != nil {
		return fmt.Errorf("failed to get a lease: %w", err)
	}
	defer done(ctx)

	image, err := c.Pull(ctx, containerName, client.WithPlatformMatcher(platform))
	if err != nil {
		return fmt.Errorf("failed to pull the container %s: %w", containerName, err)
	}

	imageSize, err := image.Usage(ctx, client.WithUsageManifestLimit(1))
	if err != nil {
		return fmt.Errorf("failed to get the size of the image: %w", err)
	}

	log.Printf("pulled %s (%d bytes)\n", image.Name(), imageSize)

	if err := buildRootfs(ctx, c, image, outFile); err != nil {
		return err
	}

	if err := resizeImage(outFile); err != nil {
		return fmt.Errorf("failed to resize the image %s: %w", outFile, err)
	}

	return bootVM(ctx, outFile, kernel, firecrackerBinary, metricsFifo)
}

// buildRootfs creates the image at rawFile, mounts it, and populates it
// with the container's filesystem. The image is always unmounted before
// returning.
func buildRootfs(ctx context.Context, c *client.Client, image client.Image, rawFile string) (err error) {
	if err := createImage(rawFile); err != nil {
		return err
	}

	mntDir, err := os.MkdirTemp("", "c2vm")
	if err != nil {
		return fmt.Errorf("failed to create mount temp dir: %w", err)
	}
	defer os.Remove(mntDir)

	if err := runCommand("mount", "-o", "loop", rawFile, mntDir); err != nil {
		return err
	}
	log.Printf("mounted %s on %s\n", rawFile, mntDir)
	defer func() {
		log.Printf("umount %s\n", mntDir)
		if uerr := runCommand("umount", mntDir); uerr != nil && err == nil {
			err = uerr
		}
	}()

	if err := extract(ctx, c, image, mntDir); err != nil {
		return fmt.Errorf("failed to extract the container: %w", err)
	}

	if err := initScript(ctx, c, image, mntDir); err != nil {
		return fmt.Errorf("failed to create init script: %w", err)
	}

	if err := extraFiles(mntDir); err != nil {
		return fmt.Errorf("failed to add extra files to the image: %w", err)
	}

	return nil
}

// runCommand runs a command, and includes its output in the error if it
// fails.
func runCommand(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func extract(ctx context.Context, c *client.Client, image client.Image, mntDir string) error {
	manifest, err := images.Manifest(ctx, c.ContentStore(), image.Target(), platform)
	if err != nil {
		return fmt.Errorf("failed to get the manifest: %w", err)
	}

	for _, desc := range manifest.Layers {
		log.Printf("extracting layer %s\n", desc.Digest.String())
		if err := extractLayer(ctx, c, desc, mntDir); err != nil {
			return fmt.Errorf("layer %s: %w", desc.Digest, err)
		}
	}

	return nil
}

func extractLayer(ctx context.Context, c *client.Client, desc ocispec.Descriptor, mntDir string) error {
	layer, err := c.ContentStore().ReaderAt(ctx, desc)
	if err != nil {
		return err
	}
	defer layer.Close()

	return archive.Untar(content.NewReader(layer), mntDir, &archive.TarOptions{NoLchown: true})
}

func createImage(rawFile string) error {
	f, err := renameio.NewPendingFile(rawFile)
	if err != nil {
		return err
	}
	defer f.Cleanup()

	if err := runCommand("fallocate", "-l", "2G", f.Name()); err != nil {
		return err
	}

	if err := runCommand("mkfs.ext4", "-F", f.Name()); err != nil {
		return err
	}

	return f.CloseAtomicallyReplace()
}

func resizeImage(rawFile string) error {
	// let's bring the image to a more reasonable size. We do this by
	// first running e2fsck on the image then we can resize the image.
	if err := runCommand("e2fsck", "-p", "-f", rawFile); err != nil {
		return err
	}

	return runCommand("resize2fs", "-M", rawFile)
}

func extraFiles(mntDir string) error {
	if err := writeToFile(filepath.Join(mntDir, "etc", "hosts"), "127.0.0.1\tlocalhost\n"); err != nil {
		return err
	}
	if err := writeToFile(filepath.Join(mntDir, "etc", "resolv.conf"), "nameserver 192.168.0.1\n"); err != nil {
		return err
	}
	return nil
}

func initScript(ctx context.Context, c *client.Client, image client.Image, mntDir string) error {
	config, err := images.Config(ctx, c.ContentStore(), image.Target(), platform)
	if err != nil {
		return err
	}

	configBlob, err := content.ReadBlob(ctx, c.ContentStore(), config)
	if err != nil {
		return err
	}
	var imageSpec ocispec.Image
	if err := json.Unmarshal(configBlob, &imageSpec); err != nil {
		return fmt.Errorf("failed to parse the image config: %w", err)
	}
	script, err := generateInitScript(imageSpec.Config)
	if err != nil {
		return err
	}

	initPath := filepath.Join(mntDir, "init.sh")
	f, err := renameio.NewPendingFile(initPath)
	if err != nil {
		return err
	}
	defer f.Cleanup()

	if _, err := f.WriteString(script); err != nil {
		return err
	}

	if err := f.Chmod(0755); err != nil {
		return err
	}

	if err := f.CloseAtomicallyReplace(); err != nil {
		return err
	}

	log.Printf("init script created")
	return nil
}

func writeToFile(filepath string, content string) error {
	if err := os.WriteFile(filepath, []byte(content), 0644); err != nil {
		return fmt.Errorf("writeToFile %s: %v", filepath, err)
	}
	return nil
}

func bootVM(ctx context.Context, rawImage, kernel, firecrackerBinary, metricsFifo string) error {
	vmmCtx, vmmCancel := context.WithCancel(ctx)
	defer vmmCancel()

	devices := make([]models.Drive, 1)
	devices[0] = models.Drive{
		DriveID:      firecracker.String("1"),
		PathOnHost:   &rawImage,
		IsRootDevice: firecracker.Bool(true),
		IsReadOnly:   firecracker.Bool(false),
	}
	fcCfg := firecracker.Config{
		LogLevel:        "debug",
		SocketPath:      firecrackerSock,
		KernelImagePath: kernel,
		KernelArgs:      "console=ttyS0 reboot=k panic=1 acpi=off pci=off i8042.noaux i8042.nomux i8042.nopnp i8042.dumbkbd init=/init.sh random.trust_cpu=on",
		Drives:          devices,
		MetricsFifo:     metricsFifo,
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(1),
			Smt:        firecracker.Bool(true),
			MemSizeMib: firecracker.Int64(512),
		},
		NetworkInterfaces: []firecracker.NetworkInterface{
			{
				CNIConfiguration: &firecracker.CNIConfiguration{
					NetworkName: "c2vm",
					IfName:      "eth0",
				},
			},
		},
	}

	machineOpts := []firecracker.Opt{}

	command := firecracker.VMCommandBuilder{}.
		WithBin(firecrackerBinary).
		WithSocketPath(fcCfg.SocketPath).
		WithStdin(os.Stdin).
		WithStdout(os.Stdout).
		WithStderr(os.Stderr).
		Build(ctx)
	machineOpts = append(machineOpts, firecracker.WithProcessRunner(command))
	m, err := firecracker.NewMachine(vmmCtx, fcCfg, machineOpts...)
	if err != nil {
		return fmt.Errorf("failed to create the vm: %w", err)
	}

	if err := m.Start(vmmCtx); err != nil {
		return fmt.Errorf("failed to start the vm: %w", err)
	}
	defer m.StopVMM()

	if err := m.Wait(vmmCtx); err != nil {
		return fmt.Errorf("vm exited with an error: %w", err)
	}
	log.Print("vm exited")
	return nil
}
