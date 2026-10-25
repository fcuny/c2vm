#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""What the kernel workflows do around update.sh and the published kernel:
decide whether there's something to do, and open the PRs.

    kernel/bot.py update-check [<version>]
    kernel/bot.py update-pr <previous version> [<changes file>]
    kernel/bot.py default

update-check picks the release to move to: <version>, or kernel.org's
latest stable release. It writes it to $GITHUB_ENV as VERSION, and to
$GITHUB_OUTPUT the current one as current, and skip=true when there's
nothing to do. update-pr runs after update.sh: it commits kernel/, pushes
it, opens the PR, and closes the ones it supersedes.

default runs after VERSION's series is published: if it's newer than
c2vm's default kernel, it opens a PR that moves the default to it, and
closes the ones it supersedes.

They run in GitHub Actions, with plain python3 (only the standard
library), and gh, authenticated with GH_TOKEN. They act on the checkout
they run from, which needn't be the one bot.py comes from: the
kernel-update workflow runs the bot.py of the branch it runs on, on main.
"""

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
import time
import urllib.request
from functools import cache
from pathlib import Path

here = Path(__file__).resolve().parent

release_re = re.compile(r"[0-9]+\.[0-9]+(\.[0-9]+)?")

# Tell the PRs update-pr and default open from others, which they don't
# close.
update_marker = "<!-- kernel-update -->"
default_marker = "<!-- kernel-default -->"

# Where default changes the default kernel's tag: cmd/c2vm/kernel.go, and
# the examples.
default_files = ["cmd/c2vm/kernel.go", "README.md", "kernel/README.md"]

# A PR description is at most 65536 characters: the config changes are
# cut, at the end of a line, past this.
max_changes = 60000


def parse(version: str) -> tuple[int, ...]:
    return tuple(int(n) for n in version.split("."))


def series(version: str) -> str:
    """7.4 for 7.4 and 7.4.1, from series.sh, like the published tags."""
    return subprocess.run([str(here / "series.sh"), version], check=True, text=True,
                          stdout=subprocess.PIPE).stdout.strip()


def update_skip(current: str, version: str, requested: bool, states: list[str]) -> str | None:
    """Why there's nothing to do to move from current to version, if
    there isn't, given the states of the PRs for version's branch.

    A release asked for by hand goes ahead, even an older one, to roll
    back, unless it's the current one or has an open PR. Every day, only a
    newer release does, and not one whose PR was closed: closing it is how
    to skip a release."""
    if version == current:
        return f"already on {version}"
    if "OPEN" in states:
        return f"there's already a PR for {version}"
    if not requested:
        if parse(version) <= parse(current):
            return f"already on {current}, which is {version} or newer"
        if "CLOSED" in states:
            return f"the PR for {version} was closed: skipping it"
    return None


def update_body(current: str, version: str, changes: str) -> str:
    lines = [
        update_marker,
        f"Moves the guest kernel from {current} to {version}, with `kernel/update.sh`.",
        "",
        "Before merging, test the kernel with `kernel/test/pr.py` and this PR's number.",
    ]
    if series(version) != series(current):
        lines += ["", f"This is a new series: once it's published, the kernel workflow opens a PR that makes `:{series(version)}` c2vm's default kernel."]
    lines.append("")
    if not changes:
        lines.append("No config changes.")
        return "\n".join(lines) + "\n"
    lines += ["<details><summary>Config changes</summary>", "", "```"]
    if len(changes) <= max_changes:
        lines += [changes.rstrip("\n"), "```"]
    else:
        lines += [*changes[:max_changes].split("\n")[:-1], "```", "", "Truncated: the workflow's log has all of them."]
    lines += ["", "</details>"]
    return "\n".join(lines) + "\n"


def default_tag(kernel_go: str) -> str | None:
    """The series of the default kernel, from cmd/c2vm/kernel.go."""
    m = re.search(r'c2vm-kernel:([0-9]+\.[0-9]+)"', kernel_go)
    return m[1] if m else None


def default_skip(current: str, series: str, states: list[str]) -> str | None:
    """Why the default kernel doesn't move from :current to :series, if it
    doesn't, given the states of the PRs for series' branch. As with
    releases, closing the PR is how to stay on the current one."""
    if parse(series) <= parse(current):
        return f"the default kernel is :{current}, which is :{series} or newer"
    if "OPEN" in states:
        return f"there's already a PR for :{series}"
    if "CLOSED" in states:
        return f"the PR for :{series} was closed: staying on :{current}"
    return None


def retag(text: str, current: str, series: str) -> str:
    """Moves the c2vm-kernel:current tags in text to series, and not the
    longer ones that start with it, like :7.2.9 or :7.20 for 7.2."""
    return re.sub(rf"c2vm-kernel:{re.escape(current)}(?!\.?[0-9])", f"c2vm-kernel:{series}", text)


def superseded(prs: list[dict], marker: str, prefix: str, version: str) -> list[int]:
    """The open PRs, from gh pr list --json number,headRefName,body, that
    a PR for version supersedes: the ones with marker, from a branch
    prefix + an older release."""
    numbers = []
    for pr in prs:
        if marker not in pr["body"] or not pr["headRefName"].startswith(prefix):
            continue
        other = pr["headRefName"].removeprefix(prefix)
        if release_re.fullmatch(other) and parse(other) < parse(version):
            numbers.append(pr["number"])
    return numbers


@cache
def repo() -> Path:
    """The top of the checkout this runs from."""
    return Path(subprocess.run(["git", "rev-parse", "--show-toplevel"], check=True, text=True,
                               stdout=subprocess.PIPE).stdout.strip())


def gh(*args: str) -> str:
    return subprocess.run(["gh", *args], cwd=repo(), check=True, text=True, stdout=subprocess.PIPE).stdout


def git(*args: str) -> None:
    subprocess.run(["git", *args], cwd=repo(), check=True)


def pr_states(branch: str) -> list[str]:
    """The states of the PRs from branch: OPEN, CLOSED or MERGED."""
    prs = json.loads(gh("pr", "list", "--head", branch, "--state", "all", "--json", "state"))
    return [pr["state"] for pr in prs]


def close_superseded(marker: str, prefix: str, version: str, by: str) -> None:
    prs = json.loads(gh("pr", "list", "--state", "open", "--limit", "200", "--json", "number,headRefName,body"))
    for number in superseded(prs, marker, prefix, version):
        gh("pr", "close", str(number), "--delete-branch", "--comment", f"Superseded by {by}.")


def push(branch: str, message: str, *paths: str) -> None:
    """Commits paths, relative to the top of the checkout, to a new
    branch, as github-actions[bot], and pushes it, replacing what's
    there."""
    gh("auth", "setup-git")
    git("config", "user.name", "github-actions[bot]")
    git("config", "user.email", "41898282+github-actions[bot]@users.noreply.github.com")
    git("switch", "-c", branch)
    git("add", "-A", *paths)
    git("commit", "-m", message)
    git("push", "--force", "origin", branch)


def set_output(name: str, value: str, var: str = "GITHUB_OUTPUT") -> None:
    with open(os.environ[var], "a") as f:
        f.write(f"{name}={value}\n")


def latest_stable() -> str:
    attempts = 3
    for attempt in range(1, attempts + 1):
        try:
            with urllib.request.urlopen("https://www.kernel.org/releases.json", timeout=30) as r:
                return json.load(r)["latest_stable"]["version"]
        # The request failing, or what it got not being releases.json.
        except (OSError, ValueError, KeyError, TypeError) as e:
            if attempt == attempts:
                sys.exit(f"can't get kernel.org's latest stable release: {e!r}")
            time.sleep(5 * attempt)
    raise AssertionError("unreachable")


def update_check(requested: str) -> None:
    version = requested
    if not version:
        version = latest_stable()
        print(f"kernel.org's latest stable release is {version}")
    if not release_re.fullmatch(version):
        sys.exit(f"not a kernel release: {version}")
    current = (repo() / "kernel" / "VERSION").read_text().strip()
    set_output("VERSION", version, "GITHUB_ENV")
    set_output("current", current)

    reason = update_skip(current, version, bool(requested), pr_states(f"kernel/{version}"))
    if reason:
        if requested:
            sys.exit(reason)
        print(reason)
        set_output("skip", "true")


def update_pr(current: str, changes: Path) -> None:
    version = os.environ["VERSION"]
    branch = f"kernel/{version}"
    push(branch, f"kernel: move to {version}", "kernel")
    body = update_body(current, version, changes.read_text() if changes.exists() else "")
    # From a file: the body can be long for an argument.
    with tempfile.NamedTemporaryFile("w", suffix=".md") as f:
        f.write(body)
        f.flush()
        gh("pr", "create", "--draft", "--base", "main", "--head", branch,
           "--title", f"kernel: move to {version}", "--body-file", f.name)
    # Not the PRs others opened, like a kernel/7.3.5 to stay on 7.3.
    close_superseded(update_marker, "kernel/", version, version)


def default() -> None:
    top = repo()
    target = series((top / "kernel" / "VERSION").read_text().strip())
    current = default_tag((top / default_files[0]).read_text())
    if not current:
        sys.exit(f"can't find the default kernel's tag in {default_files[0]}")
    branch = f"c2vm/default-kernel-{target}"
    reason = default_skip(current, target, pr_states(branch))
    if reason:
        print(reason)
        return

    for name in default_files:
        path = top / name
        text = path.read_text()
        new = retag(text, current, target)
        if name == default_files[0] and new == text:
            sys.exit(f"didn't change the default kernel in {name}")
        path.write_text(new)

    title = f"c2vm: boot {target} kernels by default"
    push(branch, title, *default_files)
    body = (f"{default_marker}\nMoves the default kernel from `:{current}` to `:{target}`, "
            "which the kernel workflow just published. Before merging, run `make e2e`.\n")
    gh("pr", "create", "--draft", "--base", "main", "--head", branch, "--title", title, "--body", body)
    close_superseded(default_marker, "c2vm/default-kernel-", target, f":{target}")


def main() -> None:
    parser = argparse.ArgumentParser(description="The kernel workflows' steps.")
    sub = parser.add_subparsers(dest="command", required=True)
    p = sub.add_parser("update-check")
    p.add_argument("version", nargs="?", default="")
    p = sub.add_parser("update-pr")
    p.add_argument("current")
    p.add_argument("changes", nargs="?", type=Path, default=Path("changes.txt"))
    sub.add_parser("default")
    args = parser.parse_args()

    if args.command == "update-check":
        update_check(args.version)
    elif args.command == "update-pr":
        update_pr(args.current, args.changes)
    elif args.command == "default":
        default()


if __name__ == "__main__":
    main()
