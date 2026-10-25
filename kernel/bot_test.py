#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.12"
# dependencies = []
# ///
"""Tests for bot.py's decisions, without gh or git:

    kernel/bot_test.py

or python3 kernel/bot_test.py, as the kernel-bot workflow runs it.
"""

import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import bot  # noqa: E402


class UpdateSkip(unittest.TestCase):
    def test_daily(self):
        cases = [
            ("7.2.9", "7.3", [], None),
            ("7.2.9", "7.2.9", [], "already on 7.2.9"),
            ("7.3", "7.2.9", [], "already on 7.3, which is 7.2.9 or newer"),
            ("7.3.1", "7.3", [], "already on 7.3.1, which is 7.3 or newer"),
            ("7.9.1", "7.10", [], None),
            ("7.2.9", "7.3", ["OPEN"], "there's already a PR for 7.3"),
            ("7.2.9", "7.3", ["CLOSED"], "the PR for 7.3 was closed: skipping it"),
            # Merged, then rolled back by hand: it's newer again.
            ("7.2.9", "7.3", ["MERGED"], None),
        ]
        for current, version, states, want in cases:
            with self.subTest(current=current, version=version, states=states):
                self.assertEqual(bot.update_skip(current, version, False, states), want)

    def test_requested(self):
        cases = [
            ("7.3", "7.2.9", [], None),
            ("7.2.9", "7.3", ["CLOSED"], None),
            ("7.3", "7.3", [], "already on 7.3"),
            ("7.3", "7.2.9", ["OPEN"], "there's already a PR for 7.2.9"),
        ]
        for current, version, states, want in cases:
            with self.subTest(current=current, version=version, states=states):
                self.assertEqual(bot.update_skip(current, version, True, states), want)


class UpdateBody(unittest.TestCase):
    def test_new_series(self):
        body = bot.update_body("7.2.9", "7.3", "A n -> y\n")
        self.assertTrue(body.startswith(bot.update_marker + "\n"))
        self.assertIn("makes `:7.3` c2vm's default kernel", body)
        self.assertIn("```\nA n -> y\n```\n", body)

    def test_same_series(self):
        body = bot.update_body("7.3", "7.3.1", "")
        self.assertNotIn("new series", body)
        self.assertIn("No config changes.", body)

    def test_truncated(self):
        line = "-SOME_OPTION y\n"
        changes = line * (bot.max_changes // len(line) + 100)
        body = bot.update_body("7.3", "7.3.1", changes)
        self.assertLess(len(body), 65536)
        self.assertIn("Truncated", body)
        # Cut at the end of a line.
        block = body.split("```\n")[1]
        self.assertTrue(all(l == line.strip() for l in block.splitlines()))


class Default(unittest.TestCase):
    def test_tag(self):
        self.assertEqual(bot.default_tag('const defaultKernel = "ghcr.io/fcuny/c2vm-kernel:7.3"\n'), "7.3")
        self.assertIsNone(bot.default_tag('const defaultKernel = "ghcr.io/fcuny/c2vm-kernel:latest"\n'))
        self.assertIsNone(bot.default_tag('const defaultKernel = "ghcr.io/fcuny/c2vm-kernel:7.3.1"\n'))

    def test_tag_in_repo(self):
        text = (Path(bot.here).parent / bot.kernel_go).read_text()
        self.assertIsNotNone(bot.default_tag(text))

    def test_newer(self):
        cases = [
            ("7.2", "7.3", None),
            ("7.9", "7.10", None),
            ("7.3", "7.3", "the default kernel is :7.3, which is :7.3 or newer"),
            ("7.4", "7.3", "the default kernel is :7.4, which is :7.3 or newer"),
        ]
        for current, target, want in cases:
            with self.subTest(current=current, target=target):
                self.assertEqual(bot.default_newer(current, target), want)

    def test_skip(self):
        cases = [
            ([], None),
            (["MERGED"], None),
            # Closed for a newer series that was then rolled back.
            (["SUPERSEDED"], None),
            (["OPEN"], "there's already a PR for :7.3"),
            (["CLOSED"], "the PR for :7.3 was closed: staying on :7.2"),
        ]
        for states, want in cases:
            with self.subTest(states=states):
                self.assertEqual(bot.default_skip("7.2", "7.3", states), want)

    def test_retag(self):
        cases = [
            ('"ghcr.io/fcuny/c2vm-kernel:7.2"', '"ghcr.io/fcuny/c2vm-kernel:7.3"'),
            ("crane export ghcr.io/fcuny/c2vm-kernel:7.2 - | tar", "crane export ghcr.io/fcuny/c2vm-kernel:7.3 - | tar"),
            ("at the end: c2vm-kernel:7.2", "at the end: c2vm-kernel:7.3"),
            ("a sentence ends with c2vm-kernel:7.2. Next", "a sentence ends with c2vm-kernel:7.3. Next"),
            ("a sentence ends the line: c2vm-kernel:7.2.\n", "a sentence ends the line: c2vm-kernel:7.3.\n"),
            ("longer tag: c2vm-kernel:7.2.9 stays", "longer tag: c2vm-kernel:7.2.9 stays"),
            ("longer series: c2vm-kernel:7.20 stays", "longer series: c2vm-kernel:7.20 stays"),
            ("other dots: c2vm-kernel:752 stays", "other dots: c2vm-kernel:752 stays"),
            # As the sed this replaced: two dots aren't the end of a tag.
            ("c2vm-kernel:7.2.. stays", "c2vm-kernel:7.2.. stays"),
            ("two lines: c2vm-kernel:7.2\nc2vm-kernel:7.2\n", "two lines: c2vm-kernel:7.3\nc2vm-kernel:7.3\n"),
        ]
        for text, want in cases:
            with self.subTest(text=text):
                self.assertEqual(bot.retag(text, "7.2", "7.3"), want)


class Superseded(unittest.TestCase):
    def test_default(self):
        marker = bot.default_marker
        prs = [
            {"number": 1, "headRefName": "c2vm/default-kernel-7.2", "body": marker},
            {"number": 2, "headRefName": "c2vm/default-kernel-7.3", "body": marker},
            {"number": 3, "headRefName": "c2vm/default-kernel-7.1", "body": "by hand"},
            {"number": 4, "headRefName": "kernel/7.1", "body": marker},
            # Not a series: someone pinning a release.
            {"number": 5, "headRefName": "c2vm/default-kernel-7.2.9", "body": marker},
        ]
        self.assertEqual(bot.superseded(prs, marker, "c2vm/default-kernel-", bot.series_re, "7.3"), [1])

    def test_update(self):
        marker = bot.update_marker
        prs = [
            {"number": 1, "headRefName": "kernel/7.3", "body": marker},
            {"number": 2, "headRefName": "kernel/7.3.2", "body": marker},
            {"number": 3, "headRefName": "kernel/7.3.1", "body": marker},
            # Someone else's.
            {"number": 4, "headRefName": "kernel/7.3.0", "body": "staying on 7.3"},
            {"number": 5, "headRefName": "kernel/next", "body": marker},
            {"number": 6, "headRefName": "c2vm/default-kernel-7.2", "body": marker},
            {"number": 7, "headRefName": "kernel/7.4", "body": marker},
        ]
        self.assertEqual(bot.superseded(prs, marker, "kernel/", bot.release_re, "7.3.2"), [1, 3])


class State(unittest.TestCase):
    def test_state(self):
        bot_comment = {"body": bot.superseded_by + ":7.4."}
        cases = [
            ({"state": "OPEN", "comments": []}, "OPEN"),
            ({"state": "MERGED", "comments": []}, "MERGED"),
            ({"state": "CLOSED", "comments": []}, "CLOSED"),
            ({"state": "CLOSED", "comments": [{"body": "not now"}]}, "CLOSED"),
            ({"state": "CLOSED", "comments": [{"body": "not now"}, bot_comment]}, "SUPERSEDED"),
        ]
        for pr, want in cases:
            with self.subTest(pr=pr):
                self.assertEqual(bot.state(pr), want)


if __name__ == "__main__":
    unittest.main()
