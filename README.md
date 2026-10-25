# c2vm

## What

c2vm boots container images as lightweight VMs.

It started as a way to understand how [fly.io](https://fly.io) runs its customers' container images as Firecracker VMs, as described in [Docker without Docker](https://fly.io/blog/docker-without-docker/). The pieces are the same: pull the image from its registry, turn its filesystem into a disk image, add a kernel and an init, and hand it all to a hypervisor.

It used to do that with containerd and Firecracker, on Linux. As I work on a macOS workstation, it now pulls images straight from their registry and boots them with Apple's Virtualization.framework.

## Use cases

Docker Desktop and `podman machine` run one long-lived Linux VM that all containers share. c2vm boots a VM per command, from any image, with its own kernel, as root unless the image sets a user, and nothing left behind in the VM when it stops (the converted image and the kernel stay cached on the Mac). That makes it handy for:

- **A disposable Linux machine for something quick**: try a Linux-only tool, without setting anything up and with a fresh root filesystem every time. For example, pulling a Fedora IoT OSTree commit, composing a new commit with an extra RPM, signing it, serving it over HTTP and deploying it into a scratch sysroot, all in one `./c2vm boot -memory 10240 fedora -- …`.
- **Checking a script against several distros**, since `c2vm boot` exits with the command's status:

  ```sh
  for i in alpine debian fedora; do ./c2vm boot -i $i -- sh < check.sh || echo "$i: failed"; done
  ```

- **Running a service from an image and reaching it from the Mac**, without a container runtime: `./c2vm boot nginx`, and open the address `c2vm-init` prints.
- **Seeing how an image behaves as a VM**, as fly.io runs them: what it assumes about PID 1, `/dev`, users and its environment when there's no container runtime around it.
- **Trying a kernel**: `-kernel` boots the same image on a different kernel or configuration.
- **Building a project from the Mac's checkout on Linux**, with the checkout mounted with `-v`: see [building bootc](docs/examples/build-on-linux.md).
- **Building with a pinned toolchain**, the same on the Mac as in CI: see [building c2vm's kernel](docs/examples/build-kernel.md).

Some limits to keep in mind:

- The writable layer is in memory, capped at half of `-memory`, so jobs that write a lot outside a `-v` mount need a big VM ([#40](https://github.com/fcuny/c2vm/issues/40)).
- It runs on macOS on Apple silicon only, and boots arm64 images only: the kernel is arm64 and there's no Rosetta, so a `-platform linux/amd64` image pulls but its binaries can't run.

## How

c2vm runs on macOS, on Apple silicon. There's nothing to set up: `make build` builds and signs `c2vm`, and builds `c2vm-init`.

```sh
make build
./c2vm boot nginx:stable-alpine3.24-perl
```

The VM gets an address from Virtualization.framework's NAT, which `c2vm-init` prints when it starts (`c2vm-init: eth0: 192.168.64.30/24`); it's reachable from the Mac at that address. Ctrl-C stops it.

A command after `--` replaces the image's command, as with `docker run`; it's passed to the image's entrypoint, if it has one:

```sh
./c2vm boot alpine -- uname -a
```

`-t` (or `-it`) runs the command interactively, as `docker run -it` does: your terminal goes in raw mode, and the console becomes the command's terminal, with your terminal's size (kept in sync over vsock when you resize the window), so Ctrl-C, job control and full-screen programs work. Ctrl-] stops the VM. `TERM` is set to `xterm-256color`, which most terminals are compatible with and most images know; `-e TERM=...` overrides it.

```sh
./c2vm boot -t alpine -- sh
```

Without `-t`, the command reads `/dev/null`. `-i` passes your stdin to it instead, as `docker run -i` does, over vsock, so the command reads EOF when your stdin ends:

```sh
cat run.sh | ./c2vm boot -i fedora -- bash
```

`-v source:target` mounts a directory of your Mac in the VM, as `docker run -v` does, with virtiofs; `:ro` makes it read-only. The source is an absolute path, or one relative to the current directory starting with `./` or `../`. Only directories can be shared, not single files. As with Docker Desktop, the files look like they belong to whichever user reads them in the VM, and what the VM writes belongs to you on the Mac:

```sh
./c2vm boot -v ./src:/src -v ./out:/out fedora -- make -C /src OUT=/out
```

`c2vm boot` exits with the command's exit status, which `c2vm-init` sends to the host over vsock before stopping the VM.

`-e KEY=value` adds a variable to the image's environment, or replaces it; `-e KEY` copies it from your shell:

```sh
./c2vm boot -e GREETING=hello alpine -- sh -c 'echo $GREETING'
```

`c2vm save` only pulls an image and converts it, and prints the path of the result:

```sh
./c2vm save nginx:stable-alpine3.24-perl
```

Run `./c2vm <command> -h` for the other options: the VM's CPUs and memory, the image's platform, the kernel, and where to cache things.

### Images

Images are pulled from their registry, with short names resolved as docker does (`nginx` is `docker.io/library/nginx:latest`). Credentials come from your docker or podman login; public images are pulled anonymously when there are none, or when they can't be looked up.

Each image is converted to a read-only ext4 filesystem, without mounting anything or needing root, and cached by digest in `~/Library/Caches/c2vm` (`-cache-dir` to change it), so booting an image again only checks which digest its tag points to.

At boot, `c2vm-init` runs from an initramfs: it mounts the image with a writable in-memory layer on top, and runs the image's entrypoint and command as the image's user, from its working directory and with its environment. Changes are lost when the VM stops.

`c2vm` looks for `c2vm-init` next to itself; use `-init` to point elsewhere. It's a static Linux binary for the Mac's architecture, which `make build` takes care of.

### Kernel

`c2vm boot` pulls a kernel of a recent stable series, the one `cmd/c2vm/kernel.go` names, built with [a minimal config for microVMs](kernel/), from `ghcr.io/fcuny/c2vm-kernel:7.3` (for arm64, with the kernel in `/boot/kernel`), and caches it. `-kernel` takes another image, or a kernel file. To extract the kernel yourself:

```sh
crane export ghcr.io/fcuny/c2vm-kernel:7.3 - | tar -x boot/kernel
```

Or with podman (the image has no command, so `create` needs a placeholder; the container is never started):

```sh
podman create --name c2vm-kernel ghcr.io/fcuny/c2vm-kernel:7.3 none
podman export c2vm-kernel | tar -x boot/kernel
podman rm c2vm-kernel
```

The kernel is built by a GitHub Actions workflow; to build it yourself, run `kernel/run.sh kernel/build.sh kernel/out`, which uses c2vm on macOS and docker elsewhere. See [`kernel/`](kernel/).

### Signing

Virtualization.framework only lets binaries signed with the `com.apple.security.virtualization` entitlement create VMs. `make build` signs `c2vm` with an ad-hoc signature, which is enough to run it locally; a binary built with `go build` alone fails to boot VMs.

### Testing

`go test ./...` runs the unit tests, which don't boot anything. `make e2e` builds c2vm and c2vm-init and boots VMs with them, from `alpine`, to check what commands see and do in the VM: their output and exit status, `-e`, `-i`, `-t` and `-v`. It needs macOS on Apple silicon and the network, and CI can't run it ([#53](https://github.com/fcuny/c2vm/issues/53)): run it before opening a PR.
