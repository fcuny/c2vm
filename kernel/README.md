# Guest kernel

The kernel c2vm boots, built for Firecracker and Apple's Virtualization.framework.

- `VERSION`: the kernel release to build.
- `configs/x86_64.config`, `configs/aarch64.config`: Firecracker's CI guest configs for 6.18, from Firecracker v1.17.0. Firecracker [supports](https://github.com/firecracker-microvm/firecracker/blob/v1.17.0/docs/kernel-policy.md) 6.18 guests until at least June 2028.
- `configs/c2vm.fragment`: what we add on top.
- `build.sh`: downloads the release from kernel.org, checks its checksum, applies the configs, and builds it.

The [kernel workflow](../.github/workflows/kernel.yml) builds both architectures on each change and, on `main`, publishes them as `ghcr.io/fcuny/c2vm-kernel:<major.minor>` and `:<version>`.

Both configs already enable virtio over MMIO (used by Firecracker) and PCI (used by Virtualization.framework), and build everything in: there are no modules to ship.

To move to a new patch release, update `VERSION`. To move to a new series, also update the configs from the matching Firecracker release, if there is one.
