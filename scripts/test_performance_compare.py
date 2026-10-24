import importlib.util
import json
import math
from pathlib import Path
import tempfile
import unittest
import subprocess
import sys


spec = importlib.util.spec_from_file_location("performance_compare", Path(__file__).with_name("performance-compare.py"))
compare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compare)


def fixture(cpu):
    return {"version": 1, "machine": {"model": "owned-fixture", "arch": "arm64"},
            "parameters": dict(duration=30, warmup=10, interval=.5, history_rows=50000, output_rate=1048576),
            "binary_sha256": "fixture-sha", "git": {"commit": "fixture"},
            "scenarios": [{"scenario": "idle", "parameters": {"output_rate_bytes_s": 0},
                "geometry": {"cols": 100, "rows": 30},
                "gui_state": {"before": {"focused": True}, "after": {"focused": True, "content_size_dip": [1000, 620]},
                              "capture_pixels": [2000, 1240]},
                "system_load": {"before": [1, 1, 1], "after": [1, 1, 1]},
                "processes": {"gui": dict(cpu_percent=cpu, footprint_mib_median=100,
                    footprint_mib_sampled_peak=150, rss_mib_median=200, rss_mib_sampled_peak=250, idle_wakeups_per_s=1)},
                "input_echo_rtt_ms": {"p50": 2, "p95": 10, "samples": [1, 2, 3, 10]},
                "snapshot_complete_ms": {"p50": 3, "p95": 11, "samples": [2, 3, 4, 11]},
                "search": None, "preview_ready_ms": None}]}


class ComparisonTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)

    def report(self, name, data):
        path = self.root / name
        path.write_text(json.dumps(data))
        return path

    def stream(self):
        return {"version": 1, "kind": "retty-stream-benchmark", "machine": {"arch": "arm64"},
                "binary_sha256": "stream-fixture", "git": {}, "parameters": {"echo_samples": 20, "go": "go1.27.2"},
                "results": [{"scenario": "slow", "route": "direct", "first_viewport_ms": 3,
                             "output_bytes_s": 1048576, "echo_ms": list(range(1, 21))}]}

    def test_encrypted_stream_quantiles_are_computed_from_raw_samples(self):
        old = self.report("old-stream.json", self.stream())
        new = self.report("new-stream.json", self.stream())
        rows, _, _ = compare.compare_reports([old], [new])
        values = {r["metric"]: r["after"]["median"] for r in rows}
        self.assertEqual(values["echo_p50"], 10.5)
        self.assertEqual(values["echo_p95"], 19)
        self.assertEqual(values["echo_p99"], 20)
        self.assertEqual(len(rows), 5)
        with self.assertRaises(ValueError):
            compare.compare_reports([old], [new], True)

    def test_stream_route_changes_failures_and_missing_samples_are_rejected(self):
        old = self.report("stream-reference.json", self.stream())
        for change in ("route", "failed", "samples", "go"):
            report = self.stream()
            if change == "route": report["results"][0]["route"] = "relay"
            elif change == "failed": report["error"] = "fixture failed"
            elif change == "go": report["parameters"]["go"] = "go1.27.3"
            else: report["results"][0]["echo_ms"].pop()
            with self.assertRaises(ValueError):
                compare.compare_reports([old], [self.report(change + ".json", report)])

    def test_repeated_trials_keep_samples_ranges_and_correct_change(self):
        before = [self.report(f"old-{i}.json", fixture(cpu)) for i, cpu in enumerate([10, 12, 14])]
        after = [self.report(f"new-{i}.json", fixture(cpu)) for i, cpu in enumerate([5, 6, 7])]
        rows, _, sources = compare.compare_reports(before, after)
        cpu = next(r for r in rows if r["metric"] == "cpu_percent")
        self.assertEqual(cpu["before"], {"n": 3, "median": 12, "min": 10, "max": 14, "samples": [10, 12, 14]})
        self.assertEqual(cpu["after"]["median"], 6)
        self.assertEqual(cpu["delta_percent"], -50)
        self.assertEqual(len(sources["before"]), 3)
        tail = next(r for r in rows if r["metric"] == "input_echo_rtt_ms/p99")
        self.assertEqual(tail["before"]["median"], 10)

    def test_split_scenarios_aggregate_only_independent_matching_workloads(self):
        old, new = [], []
        for side, output, cpu in [("old", old, 10), ("new", new, 5)]:
            for scene in ["idle", "eight_sessions"]:
                for trial in range(3):
                    report = fixture(cpu)
                    report["scenarios"][0]["scenario"] = scene
                    report["scenarios"][0]["parameters"]["sessions"] = 8 if scene == "eight_sessions" else 1
                    output.append(self.report(f"{side}-{scene}-{trial}.json", report))
        with self.assertRaises(ValueError):
            compare.compare_reports(old, new)
        rows, conditions, _ = compare.compare_reports(old, new, True)
        self.assertTrue(conditions["split_scenarios"])
        for row in rows:
            self.assertEqual(row["before"]["n"], 3)
            self.assertEqual(row["after"]["n"], 3)
        with self.assertRaises(ValueError):
            compare.compare_reports(old, new[:3], True)

    def test_startup_and_operation_resources_are_not_confused_with_steady_state(self):
        report = fixture(1)
        report["scenarios"][0]["startup"] = {"gui": dict(report["scenarios"][0]["processes"]["gui"], cpu_percent=40)}
        report["scenarios"][0]["operations"] = {"preview": {"gui": dict(report["scenarios"][0]["processes"]["gui"], cpu_percent=25)}}
        metrics = compare.report_metrics(report)
        self.assertEqual(metrics[("idle/gui", "cpu_percent", "% of one core", "lower")], 1)
        self.assertEqual(metrics[("idle/startup/gui", "cpu_percent", "% of one core", "lower")], 40)
        self.assertEqual(metrics[("idle/preview/gui", "cpu_percent", "% of one core", "lower")], 25)

    def test_changed_machine_workload_geometry_or_focus_is_rejected(self):
        old = self.report("old.json", fixture(10))
        for kind in ["machine", "duration", "geometry", "focus", "focus_mode", "workload", "failed"]:
            changed = fixture(5)
            if kind == "machine":
                changed["machine"]["arch"] = "amd64"
            elif kind == "duration":
                changed["parameters"]["duration"] = 3
            elif kind == "geometry":
                changed["scenarios"][0]["geometry"]["rows"] = 40
            elif kind == "focus":
                changed["scenarios"][0]["gui_state"]["after"]["focused"] = False
            elif kind == "focus_mode":
                changed["parameters"]["focus_mode"] = "foreground"
            elif kind == "workload":
                changed["scenarios"][0]["parameters"]["output_rate_bytes_s"] = 1048576
            else:
                changed["error"] = "fixture stopped before finishing"
            with self.subTest(kind=kind), self.assertRaises(ValueError):
                compare.compare_reports([old], [self.report(kind + ".json", changed)])

    def test_duplicates_and_nonfinite_samples_are_rejected(self):
        old = self.report("old.json", fixture(10))
        new = self.report("new.json", fixture(5))
        with self.assertRaises(ValueError):
            compare.compare_reports([old, old], [new])
        for sample in [math.nan, math.inf, -1, True]:
            with self.subTest(sample=sample), self.assertRaises(ValueError):
                compare.summary([sample])

    def test_binary_metadata_rejects_architecture_or_instrumentation_mismatch(self):
        old_dir, new_dir = self.root / "before", self.root / "after"
        old_dir.mkdir()
        new_dir.mkdir()
        old, new = old_dir / "report.json", new_dir / "report.json"
        old.write_text(json.dumps(fixture(10)))
        new.write_text(json.dumps(fixture(5)))
        metadata = "fixture: go1.27.0\n\tbuild\t-tags=mygo_noinspector\n\tbuild\tGOARCH=arm64\n"
        (old_dir / "binary-build-info.txt").write_text(metadata)
        (new_dir / "binary-build-info.txt").write_text(metadata)
        self.assertEqual(compare.compare_reports([old], [new])[1]["binary_build_settings"]["GOARCH"], "arm64")
        for changed in [metadata.replace("arm64", "amd64"), metadata + "\tbuild\t-race=true\n"]:
            (new_dir / "binary-build-info.txt").write_text(changed)
            with self.assertRaises(ValueError):
                compare.compare_reports([old], [new])

    def test_benchmarks_preserve_parallelism_and_workload_identity(self):
        header = "goos: darwin\ngoarch: arm64\ncpu: owned-fixture\npkg: owned/fixture\n"
        old = self.root / "old.txt"
        new = self.root / "new.txt"
        old.write_text(header + "BenchmarkRPC-8 100 200 ns/op 1024 B/op 4 allocs/op\n" * 3)
        new.write_text(header + "BenchmarkRPC-8 200 100 ns/op 256 B/op 2 allocs/op\n" * 3)
        rows = compare.compare_benchmarks([old], [new])
        self.assertEqual(next(r for r in rows if r["metric"] == "ns/op")["delta_percent"], -50)
        new.write_text(new.read_text().replace("RPC-8", "RPC-4"))
        with self.assertRaises(ValueError):
            compare.compare_benchmarks([old], [new])

    def test_cli_writes_json_markdown_html_and_refuses_overwrite(self):
        header = "goos: darwin\ngoarch: arm64\ncpu: fixture\npkg: owned/fixture\n"
        old, new = self.root / "old.txt", self.root / "new.txt"
        old.write_text(header + "BenchmarkRPC-8 100 200 ns/op 1024 B/op 4 allocs/op\n" * 3)
        new.write_text(header + "BenchmarkRPC-8 200 100 ns/op 256 B/op 2 allocs/op\n" * 3)
        output = self.root / "comparison"
        command = [sys.executable, str(Path(compare.__file__)), "--before-bench", str(old),
                   "--after-bench", str(new), "--output", str(output)]
        result = subprocess.run(command, capture_output=True, text=True, check=True)
        self.assertFalse(json.loads(result.stdout)["preliminary"])
        report = (output / "report.json").read_bytes()
        self.assertEqual(json.loads(report)["version"], 1)
        self.assertTrue((output / "report.md").is_file())
        self.assertTrue((output / "report.html").is_file())
        retry = subprocess.run(command, capture_output=True, text=True)
        self.assertNotEqual(retry.returncode, 0)
        self.assertEqual((output / "report.json").read_bytes(), report)

    def test_zero_baseline_and_offline_html_do_not_invent_percentages(self):
        row = compare.comparison("fixture", "cost", "B/op", "lower", [0, 0, 0], [1, 1, 1])
        self.assertIsNone(row["delta_percent"])
        report = {"sample_note": "fixture </script><script>bad()</script>", "comparisons": [row], "limits": []}
        page = compare.render_html(report)
        self.assertNotIn("fixture </script><script>", page)
        self.assertIn("\\u003c/script>", page)
        self.assertNotIn("https://", page)
        self.assertIn("—", compare.render_markdown(report))

    def test_real_api_report_comparison_keeps_request_conditions(self):
        old = {"version": 1, "kind": "retty-api-benchmark", "machine": {"arch": "arm64"},
               "parameters": {"samples": 200, "warmup": 20}, "git": {}, "binary_sha256": "old",
               "cases": [{"name": "list_full", "sessions": 8, "connection": "persistent",
                          "p50": 10, "p95": 20, "p99": 30, "mean_wire_bytes": 2048}]}
        new = json.loads(json.dumps(old))
        new["binary_sha256"] = "new"
        new["cases"][0]["p99"] = 15
        rows, _, _ = compare.compare_reports([self.report("api-old.json", old)], [self.report("api-new.json", new)])
        self.assertEqual(next(r for r in rows if r["metric"] == "p99")["delta_percent"], -50)
        new["parameters"]["samples"] = 20
        with self.assertRaises(ValueError):
            compare.compare_reports([self.root / "api-old.json"], [self.report("api-mismatch.json", new)])


if __name__ == "__main__":
    unittest.main()
