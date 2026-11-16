#!/usr/bin/env python3
"""Tests scripts/check-docs-front-door.py on a small tree: it passes when the tree is right and
each of its three checks fails on a seeded error."""
import importlib.util, os, shutil, subprocess, sys, tempfile, unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "check-docs-front-door.py")

BLOCK = "<!-- quickstart:start -->\n```sh\nbackd template project --dir ./my-app --realm demo\ndocker compose up -d\n```\n<!-- quickstart:end -->\n"
CLIENTS = "backd-js, package:backd for Dart, pkg/client for Go and backd on PyPI for Python.\n"
ISSUE = "https://github.com/fernandezvara/backd/issues/12"


def tree(**overrides):
    files = {
        "README.md": "# backd\n" + BLOCK + CLIENTS,
        "docs/content/docs/build/quickstart.md": "---\ntitle: Quickstart\n---\n" + BLOCK,
        "docs/content/docs/_index.md": CLIENTS,
        "docs/content/docs/getting-started.md": CLIENTS,
        "docs/content/docs/build/clients/_index.md": CLIENTS,
        "docs/content/docs/other.md": f"Something is Planned: [#12]({ISSUE}).\n",
    }
    files.update(overrides)
    root = tempfile.mkdtemp()
    for rel, text in files.items():
        path = os.path.join(root, rel)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "w", encoding="utf-8") as f:
            f.write(text)
    return root


def run(root):
    p = subprocess.run([sys.executable, SCRIPT, root], capture_output=True, text=True)
    return p.returncode, p.stdout


class FrontDoor(unittest.TestCase):
    def check(self, want_ok, needle=None, **overrides):
        root = tree(**overrides)
        try:
            code, out = run(root)
        finally:
            shutil.rmtree(root)
        if want_ok:
            self.assertEqual(code, 0, out)
        else:
            self.assertEqual(code, 1, out)
            self.assertIn(needle, out)

    def test_a_right_tree_passes(self):
        self.check(True)

    def test_the_readme_quick_start_must_equal_the_quickstart(self):
        self.check(False, "the quick start differs", **{"README.md": "# backd\n" + BLOCK.replace("--realm demo", "--realm other") + CLIENTS})

    def test_a_missing_marker_is_an_error(self):
        self.check(False, "no code block between", **{"README.md": "# backd\n" + CLIENTS})

    def test_a_plan_without_an_issue_fails(self):
        self.check(False, "without linking an issue", **{"docs/content/docs/other.md": "Atomic operators are planned for a later release.\n"})
        self.check(False, "without linking an issue", **{"docs/content/docs/other.md": "| Tracing | Planned |\n"})

    def test_a_plan_with_an_issue_passes_and_decisions_are_not_plans(self):
        self.check(True, **{"docs/content/docs/other.md": f"Tracing is planned: [#12]({ISSUE}).\nNot planned: Apple.\nA planned rotation keeps sessions.\n"})

    def test_code_blocks_are_not_prose(self):
        self.check(True, **{"docs/content/docs/other.md": "```\nPlanned: nothing here\n```\n"})

    def test_a_client_list_must_name_all_four_clients(self):
        self.check(False, "leave out Python", **{"docs/content/docs/getting-started.md": "backd-js, package:backd for Dart and pkg/client for Go.\n"})


class QuickstartScript(unittest.TestCase):
    GENERATOR = os.path.join(HERE, "quickstart-script.py")

    def generate(self, page):
        root = tempfile.mkdtemp()
        try:
            path = os.path.join(root, "docs", "content", "docs", "build", "quickstart.md")
            os.makedirs(os.path.dirname(path))
            with open(path, "w", encoding="utf-8") as f:
                f.write(page)
            p = subprocess.run([sys.executable, self.GENERATOR, root], capture_output=True, text=True)
            return p.returncode, p.stdout + p.stderr
        finally:
            shutil.rmtree(root)

    REAL = (
        "<!-- quickstart:start -->\n```sh\n"
        "backd template project --dir ./my-app --realm demo --no-mfa && cd my-app\n"
        "docker compose exec backd backd-server bootstrap --realm demo --email me@example.com\n"
        "backd login --realm demo --url http://127.0.0.1:8080 --email me@example.com\n"
        "KEY=$(backd apikey create --realm demo --name local --expires 1d)\n"
        "curl -X POST http://x \\\n  -H 'A: b' \\\n  -d '{}'\n"
        "```\n<!-- quickstart:end -->\n"
    )

    def test_the_commands_that_ask_for_the_password_get_it_and_exec_gets_no_tty(self):
        code, out = self.generate(self.REAL)
        self.assertEqual(code, 0, out)
        self.assertEqual(out.count('printf \'%s\\n\' "$QUICKSTART_PASSWORD" | '), 3, out)
        self.assertIn("compose exec -T backd backd-server bootstrap", out)
        self.assertIn('KEY=$(printf', out)
        self.assertIn("curl -X POST http://x    -H 'A: b'    -d '{}'", out)  # continuation lines are joined

    def test_a_change_that_loses_a_password_command_is_refused(self):
        code, out = self.generate(self.REAL.replace("backd login --realm demo --url http://127.0.0.1:8080 --email me@example.com\n", ""))
        self.assertNotEqual(code, 0)
        self.assertIn("expected 3 commands", out)

    def test_the_real_page_is_accepted(self):
        p = subprocess.run([sys.executable, self.GENERATOR], capture_output=True, text=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(p.stdout.count("QUICKSTART_PASSWORD"), 3)


if __name__ == "__main__":
    unittest.main()
