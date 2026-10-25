# Building the guest kernel with a pinned toolchain

c2vm's own kernel is built on the Mac with c2vm, from a Debian image pinned by digest. CI builds it from the same image with docker, and both end up with the same config, byte for byte:

```sh
kernel/run.sh kernel/build.sh out
```

On macOS, `run.sh` comes down to:

```sh
./c2vm boot -cpus 8 -memory 8192 -v "$root:/src" debian:trixie-slim@sha256:62bd… -- /src/kernel/builder.sh /src kernel/build.sh out
```

`builder.sh` installs the build dependencies and runs the command from the same directory as on the Mac. See [`kernel/`](../../kernel/) for the scripts.

- The image pins the packages too, not only the base. Debian's images name the snapshot.debian.org timestamp they were built from in a comment in their apt sources; `builder.sh` installs from that snapshot, so GCC only changes when the digest does. That's what lets `build.sh` compare every option of the config, toolchain ones included, and fail when they differ.
- The kernel's sources and build stay in the VM, on the writable layer, not on the `-v` mount: GNU tar can't extract the tree on virtiofs ([#57](https://github.com/fcuny/c2vm/issues/57)). They need about 3 GiB, which fits in the half of an 8 GiB VM the writable layer gets. Only the tarball cache (`kernel/downloads/`) and `out/` go on the mount, so the tarball is downloaded once.
- What the build writes on the mount is yours on the Mac, as with any `-v` mount. `builder.sh` still drops root with `setpriv`, to the owner of `/src`: with docker on Linux, that keeps `out/` from belonging to root.
- `c2vm boot` exits with the command's status, so `run.sh` fails like the docker run in CI does, and the scripts behave the same in both.
- A clean 7.3 build took 4 minutes on 8 vCPUs, including downloading the tarball and installing the packages.
