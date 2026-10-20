# Building a project from the Mac's checkout on Linux

I tested a change to [bootc](https://github.com/bootc-dev/bootc)'s Makefile (Rust, with Fedora-only build dependencies) by building it twice from the same tree and comparing what `make install` put on disk:

```sh
./c2vm boot -i -cpus 6 -memory 8192 -v ~/src/bootc:/src fedora -- bash < build.sh
```

`build.sh` installs the build dependencies with `dnf builddep`, runs `make install DESTDIR=...` with and without the change, and diffs the two trees.

- The build output goes on the `-v` mount (`target/`, with `CARGO_HOME` under it), so it outlives the VM and the next run is incremental. The writable layer only holds the packages: the 119 bootc needs fit in the 4 GiB an 8 GiB VM gets.
- The packages are installed again at every boot, since the writable layer is thrown away. An image with the build dependencies already in it avoids that.
- `set -e` in the script, plus `c2vm boot` exiting with the command's status, turns the whole run into a pass/fail check.
- A clean release build took about 4 minutes on 6 vCPUs, with `target/` on virtiofs.
