# Releasing c2vm

Pushing a `v*` tag runs the [release workflow](../.github/workflows/release.yml) on a macOS runner. It builds c2vm with `make build` and fails unless `c2vm version` reports the tag. It then creates a draft release with:

- `c2vm-<tag>-darwin-arm64.tar.gz`, which holds `c2vm` (ad-hoc signed, `c2vm-init` built in), the license and the README.
- `SHA256SUMS`.
- Notes that give the default kernel's digest at release time, followed by the pull requests merged since the previous `v*` tag.

`make build` sets the oldest macOS the binary runs on (`MACOS_MIN` in the [Makefile](../Makefile)), and the workflow checks it. Raise it when c2vm starts to need something newer.

The workflow also attests the build provenance of the tarball and of `c2vm`. The release stays a draft until someone publishes it.

## Versions

c2vm is at 0.x, so its command line can still change. New features and incompatible changes bump the minor version. Fixes alone bump the patch version.

Never reuse a version. The Go checksum database may record a tag as soon as it's pushed, and a tag moved to another commit then fails verification for anyone who fetched it.

## Steps

1. Pick a commit on `main` whose `go` workflow passed.

2. Run the end-to-end test on that commit, since CI can't boot VMs. Keep the whole log:

   ```sh
   git switch --detach <commit>
   make e2e 2>&1 | tee e2e.log
   ```

3. Tag the commit and push the tag:

   ```sh
   git tag -a v0.2.0 -m v0.2.0 <commit>
   git push origin v0.2.0
   ```

4. Wait for the release workflow to finish, then open the draft release. Edit the notes: trim or group the pull requests, and call out incompatible changes. Publish it.

5. Check what was published:

   ```sh
   gh release download v0.2.0 --repo fcuny/c2vm
   shasum -a 256 -c SHA256SUMS
   gh attestation verify c2vm-v0.2.0-darwin-arm64.tar.gz --repo fcuny/c2vm --signer-workflow fcuny/c2vm/.github/workflows/release.yml
   ```

6. Close the release's milestone, if it has one.

If the workflow fails for a reason outside the code, like a runner or a registry failing, delete any draft it left for the tag and rerun it. If the tagged commit needs a fix, fix it on `main` and release the next patch version.
