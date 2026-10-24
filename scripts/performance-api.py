#!/usr/bin/env python3
"""Measure real Retty control RPCs against an owned disposable server (macOS/Linux)."""

import argparse
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import platform
import shutil
import socket
import statistics
import sys
import time
from types import SimpleNamespace


ROOT = Path(__file__).resolve().parent.parent
spec = importlib.util.spec_from_file_location("performance_baseline", Path(__file__).with_name("performance-baseline.py"))
baseline = importlib.util.module_from_spec(spec)
spec.loader.exec_module(baseline)


class Probe:
    def __init__(self, socket_path):
        self.conn = socket.socket(socket.AF_UNIX)
        self.conn.settimeout(15)
        self.conn.connect(str(socket_path))
        self.reader = self.conn.makefile("rb")
        self.request_id = 0

    def call(self, request):
        self.request_id += 1
        wire = json.dumps({**request, "id": self.request_id}).encode() + b"\n"
        self.conn.sendall(wire)
        raw = self.reader.readline(128 * 1024 * 1024)
        if not raw.endswith(b"\n"):
            raise RuntimeError("control response incomplete or above fixture safety limit")
        response = json.loads(raw)
        if response.get("id") != self.request_id or response.get("error"):
            raise RuntimeError("control response failed: " + str(response.get("error", "wrong request ID")))
        return response, len(wire) + len(raw)

    def close(self):
        self.reader.close()
        self.conn.close()


def measure(socket_path, request, count, warmup, cached=False, fresh=False):
    samples, byte_samples, tag = [], [], ""
    probe = None
    if not fresh:
        probe = Probe(socket_path)
    try:
        for i in range(warmup + count):
            started = time.perf_counter_ns()
            if fresh:
                probe = Probe(socket_path)
            response, transferred = probe.call({**request, **({"etag": tag} if cached and tag else {})})
            elapsed = (time.perf_counter_ns() - started) / 1e6
            if cached:
                tag = response.get("etag", "")
                if not tag:
                    raise RuntimeError("cached-list fixture requires server ETag support")
            if fresh:
                probe.close()
                probe = None
            if i >= warmup:
                samples.append(elapsed)
                byte_samples.append(transferred)
    finally:
        if probe:
            probe.close()
    return {"samples": samples, "p50": statistics.median(samples),
            "p95": baseline.percentile(samples, .95), "p99": baseline.percentile(samples, .99),
            "mean_wire_bytes": statistics.mean(byte_samples), "connection": "fresh" if fresh else "persistent"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--sessions", nargs="+", type=int, default=[1, 8, 64])
    parser.add_argument("--samples", type=int, default=200)
    parser.add_argument("--warmup", type=int, default=20)
    parser.add_argument("--history-rows", type=int, default=10000)
    args = parser.parse_args()
    if platform.system() not in ("Darwin", "Linux"):
        parser.error("control fixtures currently support macOS and Linux")
    if args.samples < 20 or args.warmup < 1 or args.history_rows < 1 or any(n < 1 or n > 256 for n in args.sessions):
        parser.error("need >=20 samples, positive warmup/history, and 1..256 sessions")
    if len(set(args.sessions)) != len(args.sessions):
        parser.error("session counts must be unique")
    binary = args.binary.resolve()
    if not binary.is_file() or not os.access(binary, os.X_OK):
        parser.error("provide an existing production executable")
    args.output.mkdir(parents=True, exist_ok=False)
    if shutil.which("go"):
        info = baseline.command(["go", "version", "-m", str(binary)])
        (args.output / "binary-build-info.txt").write_text(info + "\n")
        if "-race=true" in info:
            parser.error("do not compare race-instrumented executables with production builds")
    driver_files = [Path(__file__), Path(baseline.__file__), ROOT / "scripts/perf_fixture.py"]
    report = {"version": 1, "kind": "retty-api-benchmark", "created_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
              "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "git": {},
              "machine": {"os": platform.platform(), "arch": platform.machine(), "cpus": os.cpu_count()},
              "parameters": {"sessions": args.sessions, "samples": args.samples, "warmup": args.warmup,
                             "history_rows": args.history_rows,
                             "driver_sha256": {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in driver_files}},
              "system_load": {"before": os.getloadavg()}, "server_builds": [], "cases": [],
              "limits": ["Real local Unix RPC round trips include the Python driver and JSON parsing; not GPU display latency.",
                         "Fresh-connection timing includes connect through complete decoded response; persistent timing excludes connect.",
                         "No real sessions, Codex daemon, clipboard or connection identities are used."]}
    try:
        for count in args.sessions:
            fixture = baseline.Fixture(binary, args.output, f"sessions-{count}", None, SimpleNamespace(interval=.5))
            try:
                fixture.start("server", ["-server"])
                hello = baseline.wait_for(lambda: fixture.request("hello"))
                if hello["pid"] != fixture.processes["server"].pid:
                    raise RuntimeError("server PID does not belong to the fixture")
                report["server_builds"].append({"sessions": count, "build": hello.get("build"),
                                               "protocol_version": hello.get("version")})
                sessions = [fixture.request("create", create={"command": [sys.executable, str(ROOT / "scripts/perf_fixture.py")],
                    "dir": str(fixture.workspace), "cols": 120, "rows": 38}) for _ in range(count)]
                for session in sessions:
                    baseline.wait_for(lambda s=session: b"READY" in fixture.snapshot(s["id"])[1])
                sid = sessions[0]["id"]
                fixture.workload(sid, f"HISTORY {args.history_rows}", b"HISTORY_DONE")
                for name, request, cached, fresh in [
                    ("hello", {"op": "hello"}, False, False),
                    ("list_full", {"op": "list"}, False, False),
                    ("list_cached", {"op": "list"}, True, False),
                    ("list_new_connection", {"op": "list"}, False, True),
                    ("history_1000", {"op": "history", "sid": sid, "history_rows": 1000}, False, False),
                ]:
                    result = measure(fixture.socket, request, args.samples, args.warmup, cached=cached, fresh=fresh)
                    report["cases"].append({"name": name, "sessions": count, **result})
                    print(json.dumps({"case": name, "sessions": count, "p50_ms": result["p50"], "p99_ms": result["p99"]}), flush=True)
            finally:
                fixture.close()
    except BaseException as error:
        report["error"] = f"{type(error).__name__}: {error}"
        raise
    finally:
        report["system_load"]["after"] = os.getloadavg()
        (args.output / "report.json").write_text(json.dumps(report, indent=2) + "\n")


if __name__ == "__main__":
    main()
