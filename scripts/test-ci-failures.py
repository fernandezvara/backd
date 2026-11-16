#!/usr/bin/env python3
"""Tests scripts/ci-failures.py: which failures are reported as flaky, and the issues it opens,
updates and reopens (with fakes in place of `gh`, so nothing is created)."""
import importlib.util, os, unittest

spec = importlib.util.spec_from_file_location("ci_failures", os.path.join(os.path.dirname(__file__), "ci-failures.py"))
cf = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cf)


def job(run, attempt, name, what, retried, branch="main"):
    return {"run": run, "workflow": "CI", "branch": branch, "attempt": attempt, "attempts": 2, "job": "1",
            "name": name, "what": what, "retried": retried}


class Fakes:
    def __init__(self):
        self.created, self.commented, self.reopened = [], [], []

    def kwargs(self, issues, dry_run=False):
        return dict(issues=issues, dry_run=dry_run,
                    create=lambda t, b: self.created.append((t, b)),
                    comment=lambda n, b: self.commented.append((n, b)),
                    reopen=lambda n: self.reopened.append(n))


class Report(unittest.TestCase):
    def test_only_failures_that_passed_on_a_rerun_are_flaky(self):
        jobs = [job("1", 1, "test", ["go test: TestA"], True), job("2", 1, "test", ["go test: TestB"], False)]
        self.assertEqual(list(cf.flaky_groups(jobs)), [("test", "go test: TestA")])

    def test_one_issue_per_job_and_failure_with_every_run(self):
        f = Fakes()
        jobs = [job("1", 1, "test", ["go test: TestA"], True), job("2", 1, "test", ["go test: TestA"], True, "feature")]
        done = cf.report(jobs, **f.kwargs([]))
        self.assertEqual(len(f.created), 1)
        title, body = f.created[0]
        self.assertEqual(title, "Flaky: test: go test: TestA")
        self.assertIn("/runs/1/attempts/1", body)
        self.assertIn("/runs/2/attempts/1", body)
        self.assertEqual(done, ["open: Flaky: test: go test: TestA (2 attempt(s))"])

    def test_an_open_issue_gets_only_the_runs_it_does_not_have(self):
        f = Fakes()
        have = {"number": 7, "title": "Flaky: test: go test: TestA", "state": "OPEN",
                "body": "- https://github.com/x/y/actions/runs/1/attempts/1", "comments": []}
        jobs = [job("1", 1, "test", ["go test: TestA"], True), job("3", 1, "test", ["go test: TestA"], True)]
        cf.report(jobs, **f.kwargs([have]))
        self.assertEqual(f.created, [])
        self.assertEqual(len(f.commented), 1)
        self.assertIn("/runs/3/attempts/1", f.commented[0][1])
        self.assertNotIn("/runs/1/attempts/1", f.commented[0][1])
        # Reported once: the next day's run finds nothing new.
        have["comments"] = [{"body": f.commented[0][1]}]
        f2 = Fakes()
        self.assertEqual(cf.report(jobs, **f2.kwargs([have])), [])
        self.assertEqual(f2.commented, [])

    def test_a_closed_issue_is_reopened_when_the_failure_comes_back(self):
        f = Fakes()
        closed = {"number": 9, "title": "Flaky: test: go test: TestA", "state": "CLOSED", "body": "", "comments": []}
        cf.report([job("5", 1, "test", ["go test: TestA"], True)], **f.kwargs([closed]))
        self.assertEqual(f.reopened, [9])
        self.assertEqual(len(f.commented), 1)

    def test_a_dry_run_changes_nothing(self):
        f = Fakes()
        done = cf.report([job("1", 1, "test", ["go test: TestA"], True)], **f.kwargs([], dry_run=True))
        self.assertEqual(done, ["open: Flaky: test: go test: TestA (1 attempt(s))"])
        self.assertEqual((f.created, f.commented, f.reopened), ([], [], []))

    def test_a_failure_with_no_known_cause_is_still_reported(self):
        f = Fakes()
        cf.report([job("1", 1, "shelf-tour", ["(see the log)"], True)], **f.kwargs([]))
        self.assertEqual(f.created[0][0], "Flaky: shelf-tour: failed, cause not recognized")


class Names(unittest.TestCase):
    def test_a_test_has_one_name_whatever_its_timing(self):
        self.assertEqual(cf.clean("playwright", "e2e/a.spec.ts:1:1 › x (607ms)"), cf.clean("playwright", "e2e/a.spec.ts:1:1 › x (1.9s)"))

    def test_a_whole_suite_failing_is_one_failure(self):
        found = {f"playwright: t{i}" for i in range(cf.WHOLE_SUITE)} | {"infrastructure: package mirror (apt)"}
        out = cf.collapse(found)
        self.assertEqual(len(out), 2)
        self.assertTrue(any("the whole suite failed" in f for f in out))
        few = {f"playwright: t{i}" for i in range(cf.WHOLE_SUITE - 1)}
        self.assertEqual(cf.collapse(few), few)


if __name__ == "__main__":
    unittest.main()
