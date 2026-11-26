"""Failure-oriented tests for the CI verification helpers."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location("ci", Path(__file__).with_name("ci.py"))
ci = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(ci)


class ShardTests(unittest.TestCase):
    def test_complete_disjoint_reproducible_balanced(self):
        packages = ["m/d", "m/a", "m/c", "m/b", "m/new"]
        weights = {"m/a": 10, "m/b": 6, "m/c": 4, "m/d": 1}
        shards = ci.partition(packages, weights, 2)
        self.assertEqual(shards, ci.partition(list(reversed(packages)), weights, 2))
        self.assertEqual(sorted(packages), sorted(p for shard in shards for p in shard))
        self.assertEqual(5, len(set(p for shard in shards for p in shard)))
        self.assertEqual([11, 11], [sum(weights.get(p, 1) for p in s) for s in shards])

    def test_invalid_partition_inputs(self):
        for packages, weights, count in [([], {}, 1), (["a"], {}, 0),
                                         (["a"], {}, 2), (["a", "a"], {}, 1),
                                         (["a"], {"a": -1}, 1)]:
            with self.subTest(packages=packages, count=count), self.assertRaises(ValueError):
                ci.partition(packages, weights, count)


class GateTests(unittest.TestCase):
    def test_all_success(self):
        ci.check_gate({"a": {"result": "success"}, "b": {"result": "success"}}, ["a", "b"])

    def test_rejects_failed_missing_unexpected_skipped_cancelled(self):
        for result in ["failure", "cancelled", "skipped", "", None]:
            with self.subTest(result=result), self.assertRaises(ValueError):
                ci.check_gate({"a": {"result": result}}, ["a"])
        for needs in [{}, {"a": {"result": "success"}, "b": {"result": "success"}}]:
            with self.assertRaises(ValueError):
                ci.check_gate(needs, ["a"])


class ChangeClassificationTests(unittest.TestCase):
    EMBEDS = ["internal/webadmin/assets/*", "internal/webadmin/templates/*.html", "schema/*.sql"]

    def test_only_markdown_pull_requests_skip(self):
        self.assertFalse(ci.classify("pull_request", ["README.md", "docs/x/Guide.MD"], self.EMBEDS))

    def test_everything_else_runs_full(self):
        for event, paths in [("push", ["README.md"]), ("workflow_dispatch", ["README.md"]),
                             ("schedule", []), ("pull_request", []),
                             ("pull_request", ["README.md", "go.mod"]),
                             ("pull_request", ["internal/scheduler/scheduler.go"]),
                             ("pull_request", [".github/workflows/ci.yml"]),
                             ("pull_request", ["internal/webadmin/assets/notes.md"]),
                             ("pull_request", [" README.md"]), ("pull_request", ["/README.md"]),
                             ("pull_request", ["README.md.go"])]:
            with self.subTest(event=event, paths=paths):
                self.assertTrue(ci.classify(event, paths, self.EMBEDS))

    def test_directory_embeds_cover_nested_markdown(self):
        self.assertTrue(ci.classify("pull_request", ["web/static/deep/a.md"], ["web/static"]))

    def test_quoted_embed_patterns_stay_single_tokens(self):
        self.assertEqual(["docs/user guide.md", "raw dir/*", "plain.txt"],
                         ci.embed_arguments('"docs/user guide.md" `raw dir/*` plain.txt'))
        for broken in ['"unterminated', '"bad \\q escape"', '`unterminated', '']:
            with self.subTest(broken=broken):
                self.assertIsNone(ci.embed_arguments(broken))

    def test_embed_discovery_fails_closed(self):
        with tempfile.TemporaryDirectory() as path:
            root = Path(path)
            (root / "pkg").mkdir()
            (root / "pkg" / "a.go").write_text(
                'package pkg\n//go:embed "docs/user guide.md" docs/[^x].md\n//go:embed "broken\n')
            patterns = ci.embed_patterns(root)
        self.assertIn("pkg/docs/user guide.md", patterns)
        self.assertEqual(2, patterns.count(ci.MATCH_ALL))
        self.assertTrue(ci.classify("pull_request", ["pkg/docs/user guide.md"], patterns[:1]))
        self.assertTrue(ci.classify("pull_request", ["pkg/docs/a.md"], [ci.broad_pattern("pkg/docs/[^x].md")]))
        self.assertTrue(ci.classify("pull_request", ["README.md"], [ci.MATCH_ALL]))

    def test_repository_embeds_are_discovered(self):
        root = Path(__file__).resolve().parent.parent
        patterns = ci.embed_patterns(root)
        self.assertIn("internal/webadmin/assets/*", patterns)
        self.assertIn("schema/*.sql", patterns)


class SkippableGateTests(unittest.TestCase):
    REQUIRED = ["changes", "race", "docs-tests"]

    def needs(self, full, race, docs="success", changes="success"):
        return {"changes": {"result": changes, "outputs": {"full": full}},
                "race": {"result": race}, "docs-tests": {"result": docs}}

    def test_docs_only_may_skip_skippable_jobs(self):
        ci.check_gate(self.needs("false", "skipped"), self.REQUIRED, ["race"])
        ci.check_gate(self.needs("false", "success"), self.REQUIRED, ["race"])

    def test_skips_are_rejected_otherwise(self):
        for needs in [self.needs("true", "skipped"), self.needs("", "skipped"),
                      self.needs(None, "skipped"), self.needs("false", "failure"),
                      self.needs("false", "cancelled"), self.needs("false", "skipped", docs="skipped"),
                      self.needs("false", "skipped", changes="failure")]:
            with self.subTest(needs=needs), self.assertRaises(ValueError):
                ci.check_gate(needs, self.REQUIRED, ["race"])

    def test_duplicate_job_ids_are_rejected(self):
        needs = {"a": {"result": "success"}}
        with self.assertRaises(ValueError):
            ci.check_gate(needs, ["a", "a"])
        with self.assertRaises(ValueError):
            ci.check_gate(self.needs("false", "skipped"), self.REQUIRED, ["race", "race"])

    def test_skippable_set_must_be_valid(self):
        for skippable in [["changes"], ["unknown"]]:
            with self.subTest(skippable=skippable), self.assertRaises(ValueError):
                ci.check_gate(self.needs("false", "skipped"), self.REQUIRED, skippable)


class CoverageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.packages = {"m/a": True, "m/b": False}

    def shard(self, name, packages, profile, events=None):
        directory = self.root / name
        directory.mkdir()
        (directory / "packages.txt").write_text("\n".join(packages) + "\n")
        (directory / "all-packages.txt").write_text("m/a\nm/b\n")
        (directory / "coverage.out").write_text(profile)
        if events is None:
            events = [{"Action": "pass" if self.packages[p] else "skip", "Package": p}
                      for p in packages]
        (directory / "tests.json").write_text("\n".join(json.dumps(e) for e in events))

    def test_merges_disjoint_profiles(self):
        self.shard("0", ["m/a"], "mode: atomic\nm/a/a.go:1.1,2.2 2 1\n")
        self.shard("1", ["m/b"], "mode: atomic\nm/b/b.go:1.1,2.2 1 0\n")
        rows = ci.merge_profiles(self.root, 2, self.packages)
        self.assertEqual(2, len(rows))
        self.assertEqual(3, sum(row[1] for row in rows))

    def test_distinct_numeric_ranges_count_separately(self):
        self.shard("0", ["m/a"], "mode: atomic\nm/a/a.go:1.1,2.2 2 1\nm/a/a.go:10.1,12.2 3 0\n")
        self.shard("1", ["m/b"], "mode: atomic\nm/b/b.go:1.1,2.2 1 0\n")
        self.assertEqual(3, len(ci.merge_profiles(self.root, 2, self.packages)))

    def test_missing_artifact_or_package_is_rejected(self):
        self.shard("0", ["m/a"], "mode: atomic\nm/a/a.go:1.1,2.2 2 1\n")
        with self.assertRaises(ValueError):
            ci.merge_profiles(self.root, 2, self.packages)
        with self.assertRaises(ValueError):
            ci.merge_profiles(self.root, 1, self.packages)

    def test_duplicate_package_is_rejected(self):
        for name in ["0", "1"]:
            self.shard(name, ["m/a"], "mode: atomic\nm/a/a.go:1.1,2.2 2 1\n")
        with self.assertRaises(ValueError):
            ci.merge_profiles(self.root, 2, self.packages)

    def test_profile_invalid_or_foreign(self):
        for profile in ["", "mode: set\n", "mode: atomic\n", "mode: atomic\nbad\n",
                        "mode: atomic\nm/foreign/x.go:1.1,2.2 1 1\n",
                        "mode: atomic\nm/a/a.go:1.1,2.2 1 -1\n",
                        "mode: atomic\nm/a/a.go:1.1,2.2 1 1\nm/a/a.go:1.1,2.2 1 1\n",
                        "mode: atomic\nm/a/a.go:1.1,2.2 1 1\nm/a/a.go:01.1,2.02 1 1\n"]:
            with self.subTest(profile=profile), tempfile.TemporaryDirectory() as path:
                file = Path(path) / "coverage.out"
                file.write_text(profile)
                with self.assertRaises(ValueError):
                    ci.read_profile(file, {"m/a"})

    def test_incomplete_failed_skipped_test_package_is_rejected(self):
        for events in [[], [{"Action": "fail", "Package": "m/a"}],
                       [{"Action": "skip", "Package": "m/a"}],
                       [{"Action": "pass", "Package": "m/a"},
                        {"Action": "fail", "Package": "m/a", "Test": "TestBroken"}]]:
            with self.subTest(events=events), self.assertRaises(ValueError):
                ci.check_results(events, {"m/a": True})

    def test_build_output_is_accepted_but_build_failures_are_not(self):
        passing = [{"Action": "pass", "Package": "m/a"}]
        ci.check_results([{"Action": "build-output", "ImportPath": "m/a [m/a.test]",
                           "Output": "ld: warning\n"}] + passing, {"m/a": True})
        for event in [{"Action": "build-fail", "ImportPath": "m/a [m/a.test]"},
                      {"Action": "build-output"}, {"Action": "pass"}]:
            with self.subTest(event=event), self.assertRaises(ValueError):
                ci.check_results([event] + passing, {"m/a": True})

    def test_missing_or_unknown_actions_are_rejected(self):
        passing = [{"Action": "output", "Package": "m/a", "Output": "ok\n"},
                   {"Action": "pass", "Package": "m/a"}]
        ci.check_results(passing, {"m/a": True})
        for event in [{"Package": "m/a"}, {"Package": "m/a", "Action": "explode"}]:
            with self.subTest(event=event), self.assertRaises(ValueError):
                ci.check_results([event] + passing, {"m/a": True})

    def test_no_test_package_skip_is_legitimate(self):
        ci.check_results([{"Action": "skip", "Package": "m/b"}], {"m/b": False})

    def test_stale_manifest_is_rejected(self):
        self.shard("0", ["m/a", "m/b"], "mode: atomic\nm/a/a.go:1.1,2.2 2 1\n")
        (self.root / "0" / "all-packages.txt").write_text("m/a\n")
        with self.assertRaises(ValueError):
            ci.merge_profiles(self.root, 1, self.packages)

    def test_statement_weighted_coverage_and_gates(self):
        ci.coverage([("a", 9, 1), ("b", 1, 0)], "weighted", 80)
        with self.assertRaises(ValueError):
            ci.coverage([("a", 1, 1), ("b", 9, 0)], "weighted", 40)
        with self.assertRaises(ValueError):
            ci.coverage([], "empty", 40)


if __name__ == "__main__":
    unittest.main()
