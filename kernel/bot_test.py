"""Tests for bot.py's decisions, without gh or git:

    python3 kernel/bot_test.py
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


class Superseded(unittest.TestCase):
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
        self.assertEqual(bot.superseded(prs, marker, "kernel/", "7.3.2"), [1, 3])


if __name__ == "__main__":
    unittest.main()
