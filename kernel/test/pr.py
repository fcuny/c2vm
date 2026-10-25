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
network. It exits with 1 if a check fails, and 2 if it can't test the
kernel, e.g. the PR's kernel run hasn't finished.
"""

import argparse
import os
import platform
import re
import secrets
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

root = Path(__file__).resolve().parents[2]

cpus = 4

# How long the boot may take, and how long c2vm then gets to clean up
# after a SIGTERM, as in e2e/e2e_test.go.
boot_timeout = 300
stop_timeout = 10

# What the probe prints starts with this, as the console also has
# c2vm-init's and the kernel's messages.
marker = "c2vm-probe"

# Runs in the VM, and prints a "<marker> name value" line for each thing
# it looks at, then "<marker> done". dmesg -r prefixes lines with their
# level: <3> is an error, and lower is worse.
probe = f"""
p() {{ echo "{marker} $*"; }}
p uname "$(uname -r)"
p sysctl "$(cat /proc/sys/kernel/osrelease)"
p cpus "$(nproc)"
p addr "$(ip -4 -o addr show eth0 | awk '{{print $4}}')"
if wget -q -T 10 -O /dev/null https://dl-cdn.alpinelinux.org/; then p fetch ok; fi
p root "$(awk '$2 == "/" {{t = $3}} END {{print t}}' /proc/mounts)"
p rng "$(cat /sys/devices/virtual/misc/hw_random/rng_current)"
p mount "$(cat /mnt/token)"
dmesg -r | grep -E '^<[0-3]>' | while read -r line; do p dmesg "$line"; done
p done
"""


class SetupError(Exception):
    """The kernel can't be tested."""


def output(*args: str) -> str:
    # From the checkout, so that gh acts on c2vm's repository, wherever
    # this runs from.
    return subprocess.run(args, cwd=root, check=True, text=True, stdout=subprocess.PIPE).stdout.strip()


def e2e_image() -> str:
    """The image the e2e tests boot, to boot the same one."""
    m = re.search(r'^const image = "([^"]+)"', (root / "e2e" / "e2e_test.go").read_text(), re.M)
    if not m:
        raise SetupError("can't find the image in e2e/e2e_test.go")
    return m[1]


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
        raise SetupError(f"no kernel workflow run for {sha[:7]}, #{pr}'s head")
    status, conclusion, url = found.split(" ")
    if status != "completed":
        raise SetupError(f"the kernel workflow's run for {sha[:7]} hasn't finished ({status}): {url}")
    if conclusion != "success":
        raise SetupError(f"the kernel workflow's run for {sha[:7]} didn't succeed ({conclusion}): {url}")
    return sha, version, url


def boot(kernel: Path, release: str, tmp: Path) -> tuple[list[tuple[str, bool]], list[str]]:
    """Boots the kernel and checks what it reports. Returns the checks, as
    what they check and whether it's so, and the kernel's errors."""
    token = secrets.token_hex(8)
    shared = tmp / "shared"
    shared.mkdir()
    (shared / "token").write_text(token + "\n")

    vm = subprocess.Popen(
        [str(root / "c2vm"), "boot", "-cpus", str(cpus), "-kernel", str(kernel),
         "-v", f"{shared}:/mnt:ro", e2e_image(), "--", "sh", "-c", probe],
        text=True, stdout=subprocess.PIPE,
    )
    try:
        stdout, _ = vm.communicate(timeout=boot_timeout)
        timed_out = False
    except subprocess.TimeoutExpired:
        # SIGTERM lets c2vm remove its files, which SIGKILL wouldn't.
        vm.send_signal(signal.SIGTERM)
        try:
            stdout, _ = vm.communicate(timeout=stop_timeout)
        except subprocess.TimeoutExpired:
            vm.kill()
            stdout, _ = vm.communicate()
        timed_out = True

    got: dict[str, str] = {}
    errors: list[str] = []
    for line in stdout.splitlines():
        if not line.startswith(marker + " "):
            continue
        name, _, value = line.removeprefix(marker + " ").partition(" ")
        if name == "dmesg":
            errors.append(value)
        else:
            got[name] = value

    checks = [
        (f"boots, and runs the checks within {boot_timeout}s",
         not timed_out and vm.returncode == 0 and "done" in got),
        (f"`uname -r` is `{release}`", got.get("uname") == release),
        ("`/proc/sys` is there", got.get("sysctl") == release),
        (f"sees {cpus} CPUs", got.get("cpus") == str(cpus)),
        ("gets an address over DHCP", bool(got.get("addr"))),
        ("fetches over HTTPS", got.get("fetch") == "ok"),
        ("the root is the overlay", got.get("root") == "overlay"),
        ("the random device is virtio", got.get("rng", "").startswith("virtio_rng")),
        ("reads a `-v` mount", got.get("mount") == token),
        ("no errors in dmesg", not errors),
    ]
    return checks, errors


def e2e(kernel: Path) -> tuple[bool, int, list[str]]:
    """Runs make e2e with the kernel, showing its output as it goes:
    whether it passed, how many tests did, and the ones that failed."""
    run = subprocess.Popen(["make", "e2e"], cwd=root, text=True, stdout=subprocess.PIPE,
                           stderr=subprocess.STDOUT, env={**os.environ, "C2VM_E2E_KERNEL": str(kernel)})
    assert run.stdout
    passed, failed = 0, []
    for line in run.stdout:
        sys.stderr.write(line)
        if line.startswith("--- PASS"):
            passed += 1
        elif line.startswith("--- FAIL"):
            failed.append(line.split()[2])
    return run.wait() == 0, passed, failed


def test(pr: int) -> tuple[bool, str]:
    """Tests the PR's kernel: whether it passed, and the report."""
    sha, version, url = find_run(pr)
    # uname says 7.3.0 for 7.3.
    release = (version if version.count(".") == 2 else f"{version}.0") + "-c2vm"

    with tempfile.TemporaryDirectory(prefix="c2vm-kernel-test-") as d:
        tmp = Path(d)
        print(f"downloading the kernel for {sha[:7]} from {url}", file=sys.stderr)
        subprocess.run(["gh", "run", "download", url.rsplit("/", 1)[1], "--name", "kernel",
                        "--dir", str(tmp / "artifact")], cwd=root, check=True)
        kernel = tmp / "artifact" / "arm64" / "kernel"

        print("building c2vm", file=sys.stderr)
        subprocess.run(["make", "build"], cwd=root, check=True, stdout=subprocess.DEVNULL)
        local = output("git", "describe", "--always", "--dirty")

        print("booting it", file=sys.stderr)
        checks, errors = boot(kernel, release, tmp)
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
    if errors:
        report += ["", "<details><summary>dmesg errors</summary>", "", "```", *errors, "```", "", "</details>"]
    return ok, "\n".join(report)


def main() -> None:
    parser = argparse.ArgumentParser(description="Tests the kernel a PR builds.")
    parser.add_argument("pr", type=int)
    parser.add_argument("--no-comment", action="store_true", help="only print the report")
    args = parser.parse_args()

    if sys.platform != "darwin" or platform.machine() != "arm64":
        print("booting VMs needs macOS on Apple silicon", file=sys.stderr)
        sys.exit(2)
    try:
        ok, body = test(args.pr)
    except (SetupError, subprocess.CalledProcessError) as e:
        print(e, file=sys.stderr)
        sys.exit(2)
    print(body)

    if not args.no_comment:
        if not sys.stdin.isatty():
            print(f"\nnot posting it on #{args.pr}: stdin isn't a terminal, to ask", file=sys.stderr)
        elif input(f"\nPost this on #{args.pr}? [y/N] ").strip().lower() == "y":
            subprocess.run(["gh", "pr", "comment", str(args.pr), "--body", body], cwd=root, check=True)
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
