# Building the guest kernel with a pinned toolchain

c2vm's own kernel is built on the Mac with c2vm, from a Debian image pinned by digest. CI builds it from the same image with docker, and both end up with the same config options, toolchain ones included:

```sh
kernel/run.sh kernel/build.sh kernel/out
```

On macOS, `run.sh` boots the image it names with `c2vm boot -v "$root:/src" <image> -- /src/kernel/builder.sh ...`, with the checkout on `/src`. `builder.sh` installs the build dependencies and runs the command from the same directory as on the Mac. See [`kernel/`](../../kernel/) for the scripts.

- The image pins the packages too, not only the base. Debian's images name the snapshot.debian.org timestamp they were built from in a comment in their apt sources; `builder.sh` installs from that snapshot, so GCC only changes when the digest does. That's what lets `build.sh` compare every option of the config, toolchain ones included, and fail when they differ.
- The kernel's sources and build stay in the VM, on the writable layer, not on the `-v` mount: GNU tar can't extract the tree on virtiofs ([#57](https://github.com/fcuny/c2vm/issues/57)). Only the tarball cache (`kernel/downloads/`) and `kernel/out/` go on the mount, so the tarball is downloaded once.
- The VM's size comes from the Mac's: 8 GiB of memory, or 2 GiB less than the Mac has if that's smaller, and a vCPU per GiB, up to the Mac's count. The writable layer gets half the memory, and the tree needs about 3 GiB: a 6 GiB VM, what an 8 GB Mac gets, builds it. `KERNEL_VM_MEMORY` (in MiB) and `KERNEL_VM_CPUS` override the size.
- What the build writes on the mount is yours on the Mac, as with any `-v` mount. `builder.sh` still drops root with `setpriv`, to the owner of `/src`: with docker on Linux, that keeps `kernel/out/` from belonging to root.
- `c2vm boot` exits with the command's status, so `run.sh` fails like the docker run in CI does, and the scripts behave the same in both.
- A clean 7.3 build took 4 minutes on 8 vCPUs with 8 GiB, including downloading the tarball and installing the packages.
