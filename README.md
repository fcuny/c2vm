# c2vm

## What

c2vm boots container images as lightweight VMs.

It started as a way to understand how [fly.io](https://fly.io) runs its customers' container images as Firecracker VMs, as described in [Docker without Docker](https://fly.io/blog/docker-without-docker/). The pieces are the same: pull the image from its registry, turn its filesystem into a disk image, add a kernel and an init, and hand it all to a hypervisor.

It used to do that with containerd and Firecracker, on Linux. As I work on a macOS workstation, it now pulls images straight from their registry and boots them with Apple's Virtualization.framework.

## Use cases

Docker Desktop and `podman machine` run one long-lived Linux VM that all containers share. c2vm boots a VM per command, from any image, with its own kernel, as root unless the image sets a user, and nothing left behind in the VM when it stops (the converted image and the kernel stay cached on the Mac). That makes it handy for:

- **A disposable Linux machine for something quick**: try a Linux-only tool, without setting anything up and with a fresh root filesystem every time. For example, pulling a Fedora IoT OSTree commit, composing a new commit with an extra RPM, signing it, serving it over HTTP and deploying it into a scratch sysroot, all in one `./c2vm boot -scratch 20G fedora -- …`.
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

- By default, the writable layer is in memory, capped at half of `-memory`. Jobs that write a lot outside a `-v` mount need `-scratch` (see below), or a big VM.
- On a `-v` mount, root gets no exemption from mode bits: it can't write a read-only file, remove a file from a read-only directory (`rm -rf` on a Go module cache), or even open a file it just created with mode 0. This comes from the virtiofs server, Virtualization.framework's `VZSharedDirectory`, not from c2vm ([#57](https://github.com/fcuny/c2vm/issues/57)).
  - GNU tar fails with `Cannot open: Permission denied` on a symlink whose target is absolute or has `..` in it, and on hard links to those, because it extracts them as mode-0 placeholders first. Rootfs, distro and kernel tarballs are full of them. Use `tar -P` for an archive you trust, or extract outside the mount, on the writable layer, which needs `-scratch` or a big enough `-memory` (a kernel tree takes about 3 GiB, see [building c2vm's kernel](docs/examples/build-kernel.md)).
- It runs on macOS on Apple silicon only, and boots arm64 images only: the kernel is arm64 and there's no Rosetta, so a `-platform linux/amd64` image pulls but its binaries can't run.

## How

c2vm runs on macOS, on Apple silicon. There's nothing to set up: `make build` builds `c2vm`, with `c2vm-init` in it, and signs it.

```sh
make build
./c2vm boot nginx:stable-alpine3.24-perl
```

The VM gets an address from Virtualization.framework's NAT, which `c2vm-init` prints on stderr when it starts (`c2vm-init: eth0: 192.168.64.30/24`); it's reachable from the Mac at that address. Ctrl-C stops it.

The command's stdout and stderr go to c2vm's, separately and byte for byte, as with `docker run`: `> out`, `2>` and binary output work, and the command doesn't see a terminal. If c2vm's stdout closes (`c2vm boot ... | head -1`), the command gets SIGPIPE. If the command's output arrives cut short, or c2vm can't write it (a full disk), c2vm fails with exit status 1. That's stricter than `docker run`, which only fails when its stream of output breaks before the container exits. The kernel's messages and `c2vm-init`'s go to stderr too, on a console of their own. If the guest kernel panics, c2vm stops the VM and fails with `the guest kernel panicked`.

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

`-scratch SIZE` (`64M` to `64T`, e.g. `20G`) puts the writable layer on a disk of that size instead of in memory, for jobs that write a lot: installing packages, building, pulling data. The disk is a sparse file in `$TMPDIR`, so it only takes the space the VM writes to it. `c2vm-init` formats it as ext4 with the `mkfs.ext4` that ships in the kernel image, so overlayfs, and tools like ostree, rpm and podman, get the extended attributes they need. It's thrown away with the VM, as the in-memory layer is: c2vm removes its file once the VM has started, and the space is freed when the VM stops, even if c2vm is killed. c2vm warns when `$TMPDIR` has less space free than `SIZE`; past that, the command gets I/O errors.

```sh
./c2vm boot -scratch 20G fedora -- dnf install -y gcc
```

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

Each image is converted to a read-only ext4 filesystem, without mounting anything or needing root, and cached by digest in `~/Library/Caches/c2vm` (`-cache-dir` to change it), with its configuration. Booting an image again only checks which digest its tag points to, and booting it by digest (`alpine@sha256:…`) doesn't ask the registry at all. If the registry can't answer (no network, a TLS failure, a rate limit, a server error, or no answer within 30 s), c2vm boots the digest the tag last resolved to, if it's cached, and warns that it may be out of date. Any other answer, like an unknown tag or a denied request, still fails. The kernel works the same way.

At boot, `c2vm-init` runs from an initramfs: it mounts the image with a writable layer on top, in memory or on the `-scratch` disk, and runs the image's entrypoint and command as the image's user, from its working directory and with its environment. Changes are lost when the VM stops.

`c2vm-init` is a static Linux binary for arm64, the only architecture the VMs run, which `make build` builds and embeds in `c2vm`, so `c2vm` is the only file to copy around. `c2vm version` prints the version of the one built in, or says there's none. `-init` boots another one, to try changes to it without rebuilding `c2vm`.

### Network

Each VM gets its address from the DHCP server of Virtualization.framework's NAT, which leases addresses from a pool of 253 (192.168.64.2 to .254) shared with every other VM on the Mac, from Tart or UTM too. A lease outlives its VM, by an hour or more. The server gives a MAC address its lease back, so c2vm reuses MACs: a VM takes the first of 64 slots no other running VM holds, and its MAC comes from that slot. The slots, and the random prefix of their MACs, live in `~/Library/Caches/c2vm/mac`, whatever `-cache-dir` says. c2vm only takes as many leases as the most VMs it has run at the same time. Past 64 VMs at once, the others get random MACs, and new leases. macOS may purge `~/Library/Caches`: the next VMs then get a new prefix, and new leases, while the old ones linger until they expire.

A VM also gets the same address as the VM that last held its slot, whatever image either booted. If you SSH into VMs, each image has its own host keys, so ssh refuses to connect with `REMOTE HOST IDENTIFICATION HAS CHANGED`. Skip the host key check on the command line, so it only applies to that connection, and not to a real network on the same addresses:

```sh
ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null root@192.168.64.36
```

The kernel gets the VM's address before c2vm-init starts, which then connects to c2vm. If it hasn't within 10 s, c2vm restarts the VM, which hasn't run anything yet, and gives up after 3 boots. That's for a bug in macOS: when the last VM on the NAT stops while another VM's network is being connected, macOS shuts the NAT down and never connects that VM, whose packets then go nowhere. It's rare, and only happens with VMs starting and stopping at the same time. A new VM gets a new connection.

If the pool fills up anyway, no VM gets an address: its kernel retries DHCP (`IP-Config: Reopening network devices...` on stderr) until c2vm gives up, and every boot fails until a lease expires. `/var/db/dhcpd_leases` lists the leases. Both of these fix it, and need root:

```sh
# Shorten leases to 10 minutes.
sudo defaults write /Library/Preferences/SystemConfiguration/com.apple.InternetSharing.default.plist bootpd -dict DHCPLeaseTimeSecs -int 600
# Forget every lease, for every VM on the Mac.
sudo rm /var/db/dhcpd_leases
```

### Kernel

`c2vm boot` pulls a kernel of a recent stable series, the one `cmd/c2vm/kernel.go` names, built with [a minimal config for microVMs](kernel/), from `ghcr.io/fcuny/c2vm-kernel:7.3` (for arm64, with the kernel in `/boot/kernel`, and the static `mkfs.ext4` for `-scratch` in `/sbin/mkfs.ext4`), and caches it. `-kernel` takes another image, a directory laid out like one (`boot/kernel`, and a static arm64 `sbin/mkfs.ext4` for `-scratch`), or a kernel file, which can't be used with `-scratch`. To extract the kernel yourself:

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

Virtualization.framework only lets binaries signed with the `com.apple.security.virtualization` entitlement create VMs. `make build` signs `c2vm` with an ad-hoc signature, which is enough to run it locally; a binary built with `go build` alone fails to boot VMs. So does one from `go install`, which also has no `c2vm-init` built in: use `make build`.

### Testing

`go test ./...` runs the unit tests, which don't boot anything. `make e2e` builds c2vm, with c2vm-init embedded, and boots VMs with them, from `alpine`, to check what commands see and do in the VM: their output and exit status, `-e`, `-i`, `-t`, `-v`, `-init`, `-scratch`, that VMs reuse MAC addresses, and that a VM whose init doesn't start is restarted (`C2VM_STUCK_BOOTS` makes c2vm treat its first boots as stuck). It boots the default kernel, or `C2VM_E2E_KERNEL`: an image, like `ghcr.io/fcuny/c2vm-kernel:7.3`, or by its absolute path a directory laid out like one, or a kernel file, with which the `-scratch` tests are skipped. It needs macOS on Apple silicon and the network. CI can't run it, as GitHub's macOS runners can't boot VMs: run it before opening a PR.
