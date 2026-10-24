#!/usr/bin/env python3
"""Measure a disposable production Retty GUI, server and push worker on macOS."""

import argparse
import csv
import ctypes
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import secrets
import shutil
import signal
import socket
import statistics
import struct
import subprocess
import sys
import tempfile
import threading
import time

ROOT = Path(__file__).resolve().parent.parent
SCENARIOS = ("idle", "eight_sessions", "output", "history_search", "file_preview", "dual_viewers")
MIB = 1024 * 1024
WINDOW = [1000, 620]


def command(args, cwd=ROOT, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env, text=True).strip()


def percentile(values, fraction):
    ordered = sorted(values)
    if not ordered:
        return None
    return ordered[max(0, math.ceil(len(ordered) * fraction) - 1)]


class Rusage(ctypes.Structure):
    # RUSAGE_INFO_V0, bsd/sys/resource.h. CPU counters are Mach absolute time.
    _fields_ = [("uuid", ctypes.c_uint8 * 16)] + [
        (name, ctypes.c_uint64) for name in (
            "user", "system", "idle_wakeups", "interrupt_wakeups", "pageins",
            "wired", "rss", "footprint", "start", "exit",
        )
    ]


class Timebase(ctypes.Structure):
    _fields_ = [("numer", ctypes.c_uint32), ("denom", ctypes.c_uint32)]


class ProcessSampler:
    def __init__(self):
        self.lib = ctypes.CDLL("/usr/lib/libproc.dylib", use_errno=True)
        self.lib.proc_pid_rusage.argtypes = [ctypes.c_int, ctypes.c_int, ctypes.POINTER(Rusage)]
        self.lib.proc_pid_rusage.restype = ctypes.c_int
        system = ctypes.CDLL("/usr/lib/libSystem.B.dylib")
        base = Timebase()
        if system.mach_timebase_info(ctypes.byref(base)) != 0 or not base.denom:
            raise RuntimeError("cannot read Mach timebase")
        self.ns_per_tick = base.numer / base.denom
        self.identities = {}

    def read(self, pid):
        usage = Rusage()
        if self.lib.proc_pid_rusage(pid, 0, ctypes.byref(usage)) != 0:
            raise OSError(ctypes.get_errno(), f"cannot sample owned PID {pid}")
        identity = bytes(usage.uuid), usage.start
        if self.identities.setdefault(pid, identity) != identity:
            raise RuntimeError(f"PID {pid} was reused")
        return {
            "cpu_ns": (usage.user + usage.system) * self.ns_per_tick,
            "rss_bytes": usage.rss, "footprint_bytes": usage.footprint,
            "idle_wakeups": usage.idle_wakeups,
        }


def summarize(rows):
    roles = {}
    for row in rows:
        roles.setdefault(row["role"], []).append(row)
    result = {}
    for role, samples in roles.items():
        first, last = samples[0], samples[-1]
        elapsed = last["elapsed_s"] - first["elapsed_s"]
        if elapsed <= 0:
            raise ValueError("measurement needs at least two distinct samples")
        result[role] = {
            "pid": first["pid"], "samples": len(samples), "duration_s": elapsed,
            "cpu_percent": (last["cpu_ns"] - first["cpu_ns"]) / (elapsed * 1e9) * 100,
            "footprint_mib_median": statistics.median(s["footprint_bytes"] for s in samples) / MIB,
            "footprint_mib_last": last["footprint_bytes"] / MIB,
            "footprint_mib_sampled_peak": max(s["footprint_bytes"] for s in samples) / MIB,
            "rss_mib_median": statistics.median(s["rss_bytes"] for s in samples) / MIB,
            "rss_mib_sampled_peak": max(s["rss_bytes"] for s in samples) / MIB,
            "idle_wakeups_per_s": (last["idle_wakeups"] - first["idle_wakeups"]) / elapsed,
        }
    return result


def rpc(path, payload):
    with socket.socket(socket.AF_UNIX) as conn:
        conn.settimeout(6)
        conn.connect(str(path))
        conn.sendall(json.dumps(payload).encode() + b"\n")
        with conn.makefile("rb") as stream:
            response = json.loads(stream.readline(1024 * 1024))
        if response.get("error"):
            raise RuntimeError(response["error"])
        return response


def wait_for(check, timeout=15, on_wait=None):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        if on_wait:
            on_wait()
        try:
            result = check()
            if result:
                return result
        except (OSError, RuntimeError, ValueError) as error:
            last = error
        time.sleep(0.05)
    raise TimeoutError(f"fixture did not become ready: {last}")


class Viewer:
    def __init__(self, path, sid):
        self.bytes = 0
        self.error = None
        self.thread = None
        self.stopping = False
        self.conn = socket.socket(socket.AF_UNIX)
        self.conn.settimeout(10)
        self.conn.connect(str(path))
        self.stream = self.conn.makefile("rb")
        self.conn.sendall(json.dumps({"op": "attach", "sid": sid, "screen_frames": True}).encode() + b"\n")
        try:
            response = json.loads(self.stream.readline(4096))
            if not response.get("ok") or not response.get("screen_frames"):
                raise RuntimeError("framed attachment refused")
            self.snapshot = self.frame()
        except BaseException:
            self.close()
            raise

    def exact(self, size):
        data = self.stream.read(size)
        if len(data) != size:
            raise EOFError("incomplete fixture frame")
        return data

    def frame(self):
        size, self.cols, self.rows = struct.unpack(">IHH", self.exact(8))
        if size > 32 * MIB:
            raise ValueError("fixture frame is too large")
        return self.exact(size)

    def send(self, text):
        self.conn.sendall(text.encode() + b"\n")

    def until(self, marker):
        carry = b""
        while True:
            carry = (carry + self.frame())[-8 * MIB:]
            if marker in carry:
                return

    def drain(self):
        self.conn.settimeout(None)
        def consume():
            try:
                while not self.stopping:
                    self.bytes += len(self.frame())
            except (OSError, EOFError, ValueError) as error:
                if not self.stopping:
                    self.error = str(error)
        self.thread = threading.Thread(target=consume, daemon=True)
        self.thread.start()

    def close(self):
        self.stopping = True
        try:
            self.conn.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        if self.thread:
            self.thread.join(timeout=3)
        self.stream.close()
        self.conn.close()


class Fixture:
    def __init__(self, binary, output, scenario, sampler, args):
        # Short Unix socket paths, newly allocated every time; never accept a user's data dir.
        self.home = Path(tempfile.mkdtemp(prefix="retty-perf-", dir="/tmp"))
        self.data = self.home / "data"
        self.data.mkdir(mode=0o700)
        self.workspace = self.data / "workspace"
        self.workspace.mkdir()
        self.output = output / scenario
        self.output.mkdir()
        self.token = secrets.token_hex(32)
        (self.data / "perf-fixture.json").write_text(json.dumps({"kind": "retty-performance-fixture", "token": self.token}))
        (self.data / "settings.json").write_text(json.dumps({
            "appearance": "dark", "fontSize": 11.6,
            "hideDesktopNotifications": True, "hideAgentNotifications": True,
            "hideAgentCompletionNotifications": True, "hidePhoneNotifications": True,
        }))
        self.socket = self.data / "server.sock"
        self.gui_socket = self.data / "perf.sock"
        self.binary, self.sampler, self.args = binary, sampler, args
        self.processes, self.logs, self.raw, self.viewers = {}, [], [], []
        self.started = time.monotonic()
        self.env = {k: v for k, v in os.environ.items() if not k.startswith(("RETTY_", "MYGO_"))}
        self.env.update(RETTY_DIR=str(self.data), GOWORK="off")

    def start(self, role, args, extra=None):
        log = (self.output / f"{role}.log").open("w")
        self.logs.append(log)
        self.processes[role] = subprocess.Popen(
            [str(self.binary), *args], env={**self.env, **(extra or {})},
            cwd=self.data, stdout=log, stderr=log, start_new_session=True,
        )

    def request(self, op, **params):
        return rpc(self.socket, {"id": 1, "op": op, **params}).get("data")

    def gui(self, op="status", value=""):
        return rpc(self.gui_socket, {"token": self.token, "op": op, "value": value})

    def sample(self, phase):
        for role, process in self.processes.items():
            if process.poll() is not None:
                raise RuntimeError(f"fixture {role} exited with {process.returncode}")
            row = self.sampler.read(process.pid)
            row.update(role=role, pid=process.pid, phase=phase, elapsed_s=time.monotonic() - self.started)
            self.raw.append(row)

    def hold(self, seconds, phase):
        deadline = time.monotonic() + seconds
        self.sample(phase)
        while time.monotonic() < deadline:
            time.sleep(min(self.args.interval, max(0, deadline - time.monotonic())))
            self.sample(phase)

    def viewer(self, sid):
        viewer = Viewer(self.socket, sid)
        self.viewers.append(viewer)
        return viewer

    def workload(self, sid, command_text, marker):
        viewer = self.viewer(sid)
        try:
            viewer.send(command_text)
            viewer.until(marker)
        finally:
            viewer.close()
            self.viewers.remove(viewer)

    def run(self, scenario):
        self.start("server", ["-server"])
        hello = wait_for(lambda: self.request("hello"), on_wait=lambda: self.sample("startup"))
        if hello["pid"] != self.processes["server"].pid:
            raise RuntimeError("server does not belong to this run")
        self.start("push", ["-push-service", str(self.data), str(self.socket)])
        wait_for(lambda: rpc(self.data / "push.sock", {"op": "status"}), on_wait=lambda: self.sample("startup"))
        count = 8 if scenario == "eight_sessions" else 1
        sessions = [self.request("create", create={
            "command": [sys.executable, str(ROOT / "scripts/perf_fixture.py")],
            "dir": str(self.workspace), "cols": 120, "rows": 38,
        }) for _ in range(count)]
        sid = sessions[0]["id"]
        for session in sessions:
            wait_for(lambda s=session: b"READY" in self.snapshot(s["id"])[1])
        self.request("setLayout", layout={"active": 0, "tabs": [
            {"name": f"Performance {i + 1}", "focus": 0, "root": {"sid": s["id"], "dir": str(self.workspace)}}
            for i, s in enumerate(sessions)
        ]})
        self.start("gui", [], {"RETTY_PERF_SOCKET": str(self.gui_socket)})
        wait_for(lambda: self.gui().get("tabs") == count, on_wait=lambda: self.sample("startup"))
        if self.args.focus_mode == "foreground":
            self.gui("focus")
            wait_for(lambda: self.gui().get("focused"), on_wait=lambda: self.sample("startup"))
        self.gui("capture")
        self.hold(self.args.warmup, "startup")
        if scenario == "history_search":
            self.workload(sid, f"HISTORY {self.args.history_rows}", b"HISTORY_DONE")
            self.hold(1, "setup")
            started = time.monotonic()
            self.sample("search")
            self.gui("search", "performance-needle")
            def search_ready():
                status = self.gui()
                return status if not status["search_busy"] and status["search_matches"] > 0 else None
            status = wait_for(search_ready, on_wait=lambda: self.sample("search"))
            self.sample("search")
            search = {"complete_ms": (time.monotonic() - started) * 1000, "matches": status["search_matches"]}
        else:
            search = None
        if scenario == "file_preview":
            fixture = self.workspace / "preview.go"
            fixture.write_bytes((b"// performance preview\nvar baseline = 123456789\n" * 12000)[:512 * 1024])
            for i in range(100):
                (self.workspace / f"file-{i:03}.txt").write_text("fixture\n")
            started = time.monotonic()
            self.sample("preview")
            self.gui("preview", str(fixture))
            wait_for(lambda: self.gui().get("preview_ready"), on_wait=lambda: self.sample("preview"))
            self.sample("preview")
            preview_ms = (time.monotonic() - started) * 1000
        else:
            preview_ms = None
        if scenario == "dual_viewers":
            self.viewer(sid).drain()
        latency = self.input_latency(sid)
        snapshots = [self.snapshot(sid)[0] for _ in range(10)]
        if scenario in ("output", "dual_viewers"):
            self.workload(sid, f"OUTPUT {self.args.output_rate}", b"OUTPUT_STARTED")
        self.hold(1, "settle")
        gui_before = self.gui()
        if self.args.focus_mode == "foreground" and not gui_before["focused"]:
            raise RuntimeError("fixture lost foreground focus before measurement; do not compare this run")
        load_before = os.getloadavg()
        before = self.request("list")[0]["output"]
        viewer_before = [viewer.bytes for viewer in self.viewers]
        start = time.monotonic()
        self.hold(self.args.duration, "measurement")
        elapsed = time.monotonic() - start
        after = self.request("list")
        gui_after = self.gui()
        if self.args.focus_mode == "foreground" and not gui_after["focused"]:
            (self.output / "invalid-focus.json").write_text(json.dumps({"before": gui_before, "after": gui_after}, indent=2))
            self.gui("capture")
            shutil.copyfile(self.data / "capture.png", self.output / "invalid-focus.png")
            raise RuntimeError("fixture lost foreground focus during measurement; do not compare this run")
        throughput = (after[0]["output"] - before) / elapsed
        viewer_bytes = [viewer.bytes - initial for viewer, initial in zip(self.viewers, viewer_before)]
        if scenario in ("output", "dual_viewers"):
            self.workload(sid, "STOP", b"OUTPUT_STOPPED")
            if throughput < self.args.output_rate * 0.8:
                raise RuntimeError(f"output workload fell below 80% of configured rate: {throughput}")
        for viewer in self.viewers:
            if viewer.error:
                raise RuntimeError(f"protocol viewer failed: {viewer.error}")
        if scenario == "dual_viewers" and any(count < (after[0]["output"] - before) * .8 for count in viewer_bytes):
            raise RuntimeError("second viewer did not consume the output workload")
        self.gui("capture")
        shutil.copyfile(self.data / "capture.png", self.output / "gui.png")
        pixels = list(struct.unpack(">II", (self.output / "gui.png").read_bytes()[16:24]))
        return {
            "scenario": scenario, "parameters": {"sessions": count, "window_dip": WINDOW,
                "history_rows": self.args.history_rows if search else 0,
                "output_rate_bytes_s": self.args.output_rate if scenario in ("output", "dual_viewers") else 0,
                "extra_protocol_viewers": int(scenario == "dual_viewers")},
            "processes": summarize([r for r in self.raw if r["phase"] == "measurement"]),
            "startup": summarize([r for r in self.raw if r["phase"] == "startup"]),
            "operations": {phase: summarize([r for r in self.raw if r["phase"] == phase])
                for phase in ("search", "preview") if any(r["phase"] == phase for r in self.raw)},
            "input_echo_rtt_ms": {"samples": latency, "p50": statistics.median(latency), "p95": percentile(latency, .95)},
            "snapshot_complete_ms": {"samples": snapshots, "p50": statistics.median(snapshots), "p95": percentile(snapshots, .95)},
            "output_bytes_s": throughput, "search": search, "preview_ready_ms": preview_ms,
            "geometry": {"cols": after[0]["cols"], "rows": after[0]["rows"]},
            "gui_state": {"before": gui_before, "after": gui_after, "capture_pixels": pixels},
            "system_load": {"before": load_before, "after": os.getloadavg()},
            "extra_viewer_bytes": viewer_bytes,
        }

    def snapshot(self, sid):
        start = time.monotonic()
        viewer = self.viewer(sid)
        try:
            return (time.monotonic() - start) * 1000, viewer.snapshot
        finally:
            viewer.close()
            self.viewers.remove(viewer)

    def input_latency(self, sid):
        viewer = self.viewer(sid)
        result = []
        try:
            for i in range(20):
                start = time.monotonic()
                viewer.send(f"ECHO {i}")
                viewer.until(f"PERF_ECHO_{i}\r\n".encode())
                result.append((time.monotonic() - start) * 1000)
        finally:
            viewer.close()
            self.viewers.remove(viewer)
        return result

    def close(self):
        for viewer in self.viewers:
            viewer.close()
        if "gui" in self.processes and self.processes["gui"].poll() is None:
            try:
                self.gui("quit")
                self.processes["gui"].wait(timeout=5)
            except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired):
                pass
        # Every PID below came from this runner's own Popen; no process-name kills.
        if "server" in self.processes and self.processes["server"].poll() is None:
            try:
                self.request("shutdown")
                self.processes["server"].wait(timeout=5)
            except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired):
                pass
        for process in self.processes.values():
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
        for log in self.logs:
            log.close()
        if self.raw:
            with (self.output / "samples.csv").open("w", newline="") as target:
                writer = csv.DictWriter(target, fieldnames=self.raw[0].keys())
                writer.writeheader()
                writer.writerows(self.raw)
        shutil.rmtree(self.home)


def render_report(report):
    lines = ["# Retty performance baseline", "", f"Run: {report['created_at']}",
        f"Commit: `{report['git']['commit']}` · dirty: `{report['git']['dirty']}`",
        f"Machine: {report['machine']['model']} · {report['machine']['os']} · {report['machine']['arch']}",
        "", "CPU 100% = one core. Memory is MiB. Peaks are sampled, not lifetime maxima.", "",
        "| Scenario | Role | CPU % | Footprint median | Footprint peak | RSS peak |",
        "| --- | --- | ---: | ---: | ---: | ---: |"]
    for scene in report["scenarios"]:
        for role, stats in scene["processes"].items():
            lines.append(f"| {scene['scenario']} | {role} | {stats['cpu_percent']:.2f} | {stats['footprint_mib_median']:.2f} | {stats['footprint_mib_sampled_peak']:.2f} | {stats['rss_mib_sampled_peak']:.2f} |")
    lines.extend(["", "| Scenario | Echo P50/P95 ms | Snapshot P50/P95 ms | Output MiB/s |", "| --- | ---: | ---: | ---: |"])
    for scene in report["scenarios"]:
        echo, snap = scene["input_echo_rtt_ms"], scene["snapshot_complete_ms"]
        lines.append(f"| {scene['scenario']} | {echo['p50']:.2f} / {echo['p95']:.2f} | {snap['p50']:.2f} / {snap['p95']:.2f} | {scene['output_bytes_s'] / MIB:.3f} |")
    lines.extend(["", "## Startup sampled peaks", "",
        "| Scenario | GUI MiB | Server MiB | Push MiB |", "| --- | ---: | ---: | ---: |"])
    for scene in report["scenarios"]:
        stats = scene["startup"]
        lines.append(f"| {scene['scenario']} | {stats['gui']['footprint_mib_sampled_peak']:.2f} | {stats['server']['footprint_mib_sampled_peak']:.2f} | {stats['push']['footprint_mib_sampled_peak']:.2f} |")
    for scene in report["scenarios"]:
        if scene["search"]:
            lines.append(f"\nSearch ready: {scene['search']['complete_ms']:.2f} ms; {scene['search']['matches']} matches.")
        if scene["preview_ready_ms"] is not None:
            lines.append(f"\nPreview ready: {scene['preview_ready_ms']:.2f} ms.")
    lines.extend(["", "## GUI conditions", "",
        "| Scenario | Focus before/after | Content DIP | Capture pixels |", "| --- | --- | --- | --- |"])
    for scene in report["scenarios"]:
        state = scene["gui_state"]
        lines.append(f"| {scene['scenario']} | {state['before']['focused']} / {state['after']['focused']} | {state['after']['content_size_dip']} | {state['capture_pixels']} |")
    lines.extend(["", "## Measurement limits", "", *[f"- {s}" for s in report["limits"]], ""])
    if report.get("error"):
        lines.extend(["Run failed: " + report["error"], ""])
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--app", type=Path, default=ROOT / "build/darwin-arm64/Retty.app")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--duration", type=float, default=30)
    parser.add_argument("--warmup", type=float, default=10)
    parser.add_argument("--interval", type=float, default=.5)
    parser.add_argument("--history-rows", type=int, default=50000)
    parser.add_argument("--output-rate", type=int, default=MIB)
    parser.add_argument("--scenarios", nargs="+", choices=SCENARIOS, default=list(SCENARIOS))
    parser.add_argument("--skip-benchmarks", action="store_true")
    parser.add_argument("--focus-mode", choices=("foreground", "as-is"), default="foreground")
    args = parser.parse_args()
    def interrupted(signum, frame):
        raise KeyboardInterrupt(f"received signal {signum}")
    signal.signal(signal.SIGTERM, interrupted)
    if platform.system() != "Darwin":
        parser.error("process baseline currently supports macOS only")
    if not all(math.isfinite(v) and v > 0 for v in (args.duration, args.warmup, args.interval)) or args.duration < args.interval or args.history_rows <= 0 or args.output_rate <= 0:
        parser.error("durations must be positive and duration >= interval; workloads must be positive")
    binary = args.app.resolve() / "Contents/MacOS/Retty"
    info = command(["go", "version", "-m", str(binary)])
    if "mygo_noinspector" not in info or "-race=true" in info:
        parser.error("use the production bundle from GOWORK=off go tool mygo build")
    binary_bytes = binary.read_bytes()
    if b"RETTY_PERF_SOCKET" not in binary_bytes:
        parser.error("bundle lacks the isolated fixture entrypoint; rebuild the current source first")
    binary_sha256 = hashlib.sha256(binary_bytes).hexdigest()
    del binary_bytes
    output = (args.output or ROOT / "build/performance" / time.strftime("%Y%m%d-%H%M%S")).resolve()
    output.mkdir(parents=True, exist_ok=False)
    (output / "binary-build-info.txt").write_text(info + "\n")
    report = {"version": 1, "created_at": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "git": {"commit": command(["git", "rev-parse", "HEAD"]), "dirty": bool(command(["git", "status", "--porcelain"])),
            "tracked_diff_sha256": hashlib.sha256(command(["git", "diff", "HEAD"]).encode()).hexdigest(),
            "untracked_sha256": {p: hashlib.sha256((ROOT / p).read_bytes()).hexdigest()
                for p in command(["git", "ls-files", "--others", "--exclude-standard"]).splitlines()
                if (ROOT / p).is_file()}},
        "machine": {"os": platform.platform(), "model": command(["sysctl", "-n", "hw.model"]),
            "arch": platform.machine(), "cpus": os.cpu_count(), "memory_bytes": int(command(["sysctl", "-n", "hw.memsize"]))},
        "binary_sha256": binary_sha256,
        "parameters": {k: str(v) if isinstance(v, Path) else v for k, v in vars(args).items()},
        "scenarios": [], "limits": [
            "Fresh fixture data per scenario; production code, updater checks disabled; no RETTY_DEBUG polling.",
            "Only GUI/server/push PIDs are counted. Python PTY workloads, sampler and system WindowServer are excluded.",
            "Startup is sampled from owned process launch through readiness/warmup; short peaks between samples can be missed.",
            "Echo/snapshot probes run before the CPU/memory measurement window; they measure local framed protocol, not display latency.",
            "dual_viewers adds a local framed protocol consumer; this is not an iPhone, radio network or encrypted tunnel benchmark.",
            "Search/preview readiness is polled at 50 ms and includes controller/UI scheduling; it is not GPU frame presentation time.",
            "Search status probes also advance bounded incremental scanning; readiness is a controller-driven metric.",
            "Existing BenchmarkFrame uses ui.Tester; it measures terminal/layout frame work, not native GPU presentation.",
        ]}
    sampler = ProcessSampler()
    report["sampler"] = {"api": "proc_pid_rusage", "rusage_version": 0,
        "cpu_ns_per_mach_tick": sampler.ns_per_tick, "cpu_percent_one_core": 100,
        "peak_kind": "sampled", "memory_unit": "MiB"}
    try:
        for scenario in args.scenarios:
            print(f"Measuring {scenario}…", flush=True)
            fixture = Fixture(binary, output, scenario, sampler, args)
            try:
                result = fixture.run(scenario)
                report["scenarios"].append(result)
                (fixture.output / "result.json").write_text(json.dumps(result, indent=2) + "\n")
            finally:
                fixture.close()
        if not args.skip_benchmarks:
            env = {**os.environ, "GOWORK": "off"}
            bench = ["go", "test", "-tags", "mygo_noinspector", "-ldflags", "-X github.com/egoist/mygo.production=1",
                "./internal/terminal", "-run", "^$", "-bench", "^Benchmark(Output|Frame)$", "-benchmem", "-count", "3", "-benchtime", "1s"]
            with (output / "benchmarks.txt").open("w") as log:
                subprocess.run(bench, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
            report["benchmark_command"] = bench
    except BaseException as error:
        report["error"] = f"{type(error).__name__}: {error}"
        raise
    finally:
        (output / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        (output / "report.md").write_text(render_report(report))
        print(f"Report: {output / 'report.md'}", flush=True)


if __name__ == "__main__":
    main()
