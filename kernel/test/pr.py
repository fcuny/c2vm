#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""Tests the kernel a PR builds, and reports the result on the PR.

    kernel/test/pr.py <PR> [--no-comment]

Downloads the kernel artifact from the kernel workflow's run for the PR's
head commit, boots it with c2vm, built from this checkout, to check what
c2vm needs from it, and runs make e2e with it. Prints a report, then asks
before posting it on the PR. It needs macOS on Apple silicon, gh and the
network, and exits with 1 if a check fails.
"""

import argparse
import os
import platform
import secrets
import subprocess
import sys
import tempfile
from pathlib import Path

root = Path(__file__).resolve().parents[2]

# As in e2e/e2e_test.go: pinned, and from ECR's mirror of Docker's
# official images, to stay clear of Docker Hub's anonymous limit.
image = "public.ecr.aws/docker/library/alpine:3.24"

cpus = 4

# Runs in the VM, and prints a "name value" line for each thing it looks
# at.
probe = """
echo "uname $(uname -r)"
echo "sysctl $(cat /proc/sys/kernel/osrelease)"
echo "cpus $(nproc)"
echo "addr $(ip -4 -o addr show eth0 | awk '{print $4}')"
if wget -q -T 10 -O /dev/null https://dl-cdn.alpinelinux.org/; then echo "fetch ok"; fi
echo "root $(awk '$2 == "/" {t = $3} END {print t}' /proc/mounts)"
echo "rng $(cat /sys/devices/virtual/misc/hw_random/rng_current)"
echo "mount $(cat /mnt/token)"
dmesg | grep -iE 'error|fail|warn' | sed 's/^/dmesg /'
"""


def output(*args: str) -> str:
    return subprocess.run(args, check=True, text=True, stdout=subprocess.PIPE).stdout.strip()


def find_run(pr: int) -> tuple[str, str, str]:
    """The PR's head commit, its kernel version, and the URL of the kernel
    workflow's run for that commit, which must have succeeded."""
    sha = output("gh", "pr", "view", str(pr), "--json", "headRefOid", "--jq", ".headRefOid")
    version = output("gh", "api", "-H", "Accept: application/vnd.github.raw",
                     f"repos/{{owner}}/{{repo}}/contents/kernel/VERSION?ref={sha}")
    found = output("gh", "run", "list", "--workflow", "kernel.yml", "--commit", sha,
                   "--event", "pull_request", "--limit", "1",
                   "--json", "status,conclusion,url", "--jq", r'.[] | "\(.status) \(.conclusion) \(.url)"')
    if not found:
        sys.exit(f"no kernel workflow run for {sha[:7]}, #{pr}'s head")
    status, conclusion, url = found.split(" ")
    if status != "completed":
        sys.exit(f"the kernel workflow is still running for {sha[:7]}: {url}")
    if conclusion != "success":
        sys.exit(f"the kernel workflow didn't succeed for {sha[:7]} ({conclusion}): {url}")
    return sha, version, url


def boot(kernel: Path, release: str, tmp: Path) -> tuple[list[tuple[str, bool]], list[str]]:
    """Boots the kernel and checks what it reports. Returns the checks, as
    what they check and whether it's so, and the dmesg lines that look
    like errors."""
    token = secrets.token_hex(8)
    shared = tmp / "shared"
    shared.mkdir()
    (shared / "token").write_text(token + "\n")

    vm = subprocess.run(
        [str(root / "c2vm"), "boot", "-cpus", str(cpus), "-kernel", str(kernel),
         "-v", f"{shared}:/mnt:ro", image, "--", "sh", "-c", probe],
        text=True, stdout=subprocess.PIPE, timeout=300,
    )
    got: dict[str, str] = {}
    dmesg: list[str] = []
    for line in vm.stdout.splitlines():
        name, _, value = line.partition(" ")
        if name == "dmesg":
            dmesg.append(value)
        else:
            got[name] = value

    checks = [
        ("boots, and the command exits with 0", vm.returncode == 0),
        (f"`uname -r` is `{release}`", got.get("uname") == release),
        ("`/proc/sys` is there", got.get("sysctl") == release),
        (f"sees {cpus} CPUs", got.get("cpus") == str(cpus)),
        ("gets an address over DHCP", bool(got.get("addr"))),
        ("fetches over HTTPS", got.get("fetch") == "ok"),
        ("the root is the overlay", got.get("root") == "overlay"),
        ("the random device is virtio", got.get("rng", "").startswith("virtio_rng")),
        ("reads a `-v` mount", got.get("mount") == token),
        ("no errors or warnings in dmesg", not dmesg),
    ]
    return checks, dmesg


def e2e(kernel: Path) -> tuple[bool, int, list[str]]:
    """Runs make e2e with the kernel: whether it passed, how many tests
    did, and the ones that failed."""
    run = subprocess.run(["make", "e2e"], cwd=root, text=True, stdout=subprocess.PIPE,
                         stderr=subprocess.STDOUT, env={**os.environ, "C2VM_E2E_KERNEL": str(kernel)})
    lines = run.stdout.splitlines()
    if run.returncode != 0:
        print(run.stdout, file=sys.stderr)
    passed = sum(line.startswith("--- PASS") for line in lines)
    failed = [line.split()[2] for line in lines if line.startswith("--- FAIL")]
    return run.returncode == 0, passed, failed


def main() -> None:
    parser = argparse.ArgumentParser(description="Tests the kernel a PR builds.")
    parser.add_argument("pr", type=int)
    parser.add_argument("--no-comment", action="store_true", help="only print the report")
    args = parser.parse_args()

    if sys.platform != "darwin" or platform.machine() != "arm64":
        sys.exit("booting VMs needs macOS on Apple silicon")

    sha, version, url = find_run(args.pr)
    # uname says 7.3.0 for 7.3.
    release = (version if version.count(".") == 2 else f"{version}.0") + "-c2vm"

    with tempfile.TemporaryDirectory(prefix="c2vm-kernel-test-") as d:
        tmp = Path(d)
        print(f"downloading the kernel for {sha[:7]} from {url}", file=sys.stderr)
        subprocess.run(["gh", "run", "download", url.rsplit("/", 1)[1], "--name", "kernel",
                        "--dir", str(tmp / "artifact")], check=True)
        kernel = tmp / "artifact" / "arm64" / "kernel"

        print("building c2vm", file=sys.stderr)
        subprocess.run(["make", "build"], cwd=root, check=True, stdout=subprocess.DEVNULL)
        local = output("git", "-C", str(root), "rev-parse", "--short", "HEAD")

        print("booting it", file=sys.stderr)
        checks, dmesg = boot(kernel, release, tmp)
        print("running make e2e with it", file=sys.stderr)
        e2e_ok, e2e_passed, e2e_failed = e2e(kernel)

    ok = all(ok for _, ok in checks) and e2e_ok
    mark = {True: "✅", False: "❌"}
    e2e_result = f"{e2e_passed} passed" + (f", failed: {', '.join(e2e_failed)}" if e2e_failed else "")
    report = [
        f"Tested {version} from [the kernel workflow's run]({url}) for {sha[:7]}, "
        f"with `kernel/test/pr.py` and c2vm at {local}: "
        + ("everything passed." if ok else "some checks failed."),
        "",
        "| Check | |",
        "|---|---|",
        *(f"| {what} | {mark[passed]} |" for what, passed in checks),
        f"| `make e2e`: {e2e_result} | {mark[e2e_ok]} |",
    ]
    if dmesg:
        report += ["", "<details><summary>dmesg</summary>", "", "```", *dmesg, "```", "", "</details>"]
    body = "\n".join(report)
    print(body)

    if not args.no_comment and sys.stdin.isatty():
        if input(f"\nPost this on #{args.pr}? [y/N] ").strip().lower() == "y":
            subprocess.run(["gh", "pr", "comment", str(args.pr), "--body", body], check=True)
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
