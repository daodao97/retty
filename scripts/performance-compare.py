#!/usr/bin/env python3
"""Compare repeatable Retty reports and Go benchmarks; emit JSON, Markdown and offline HTML."""

import argparse
import json
import math
from pathlib import Path
import statistics


def summary(values):
    if not values or any(isinstance(v, bool) or not isinstance(v, (int, float)) or not math.isfinite(v) or v < 0 for v in values):
        raise ValueError("performance samples must be finite nonnegative numbers")
    return {"n": len(values), "median": statistics.median(values),
            "min": min(values), "max": max(values), "samples": values}


def comparison(scope, metric, unit, direction, before, after):
    old, new = summary(before), summary(after)
    delta = new["median"] - old["median"]
    return {"scope": scope, "metric": metric, "unit": unit, "direction": direction,
            "before": old, "after": new, "delta": delta,
            "delta_percent": delta / old["median"] * 100 if old["median"] else None}


def conditions(report):
    parameters = report["parameters"]
    if report.get("kind") == "retty-stream-benchmark":
        return {"kind": report["kind"], "machine": report["machine"], "parameters": parameters,
                "cases": sorted((c["scenario"], c["route"]) for c in report["results"])}
    if report.get("kind") == "retty-api-benchmark":
        return {"kind": report["kind"], "machine": report["machine"], "parameters": parameters,
                "cases": sorted((c["name"], c["sessions"], c["connection"]) for c in report["cases"])}
    return {"machine": report["machine"],
            "parameters": {**{k: parameters[k] for k in
                           ("duration", "warmup", "interval", "history_rows", "output_rate")},
                           "focus_mode": parameters.get("focus_mode", "as-is")},
            "scenarios": {s["scenario"]: {
                "workload": s["parameters"], "geometry": s["geometry"],
                "content_size_dip": s["gui_state"]["after"]["content_size_dip"],
                "capture_pixels": s["gui_state"]["capture_pixels"],
                "focus": [s["gui_state"][p]["focused"] for p in ("before", "after")],
                "search_matches": s["search"]["matches"] if s.get("search") else None,
                "roles": sorted(s["processes"]),
            } for s in report["scenarios"]}}


def report_metrics(report):
    result = {}
    if report.get("kind") == "retty-stream-benchmark":
        for case in report["results"]:
            scope = "encrypted-stream/" + case["scenario"]
            result[(scope, "first_viewport_ms", "ms", "lower")] = case["first_viewport_ms"]
            result[(scope, "output_bytes_s", "B/s", "higher")] = case["output_bytes_s"]
            samples = sorted(case["echo_ms"])
            if len(samples) != report["parameters"]["echo_samples"] or not samples:
                raise ValueError("encrypted stream echo sample coverage differs")
            summary(samples)
            for q in (.5, .95, .99):
                value = statistics.median(samples) if q == .5 else samples[math.ceil(len(samples)*q)-1]
                result[(scope, "echo_p" + str(int(q*100)), "ms", "lower")] = value
        return result
    if report.get("kind") == "retty-api-benchmark":
        for case in report["cases"]:
            scope = f"api/sessions={case['sessions']}/{case['name']}"
            for quantile in ("p50", "p95", "p99"):
                result[(scope, quantile, "ms", "lower")] = case[quantile]
            result[(scope, "mean_wire_bytes", "B/request", "lower")] = case["mean_wire_bytes"]
        return result
    for scene in report["scenarios"]:
        name = scene["scenario"]
        phases = [(name, scene["processes"]), (name + "/startup", scene.get("startup", {}))]
        phases.extend((name + "/" + phase, roles) for phase, roles in scene.get("operations", {}).items())
        for scope, roles in phases:
            for role, stats in roles.items():
                for metric, unit in (("cpu_percent", "% of one core"),
                                     ("footprint_mib_median", "MiB"),
                                     ("footprint_mib_sampled_peak", "MiB"),
                                     ("rss_mib_median", "MiB"), ("rss_mib_sampled_peak", "MiB"),
                                     ("idle_wakeups_per_s", "/s")):
                    result[(scope + "/" + role, metric, unit, "lower")] = stats[metric]
        for probe in ("input_echo_rtt_ms", "snapshot_complete_ms"):
            for quantile in ("p50", "p95"):
                result[(name + "/protocol", probe + "/" + quantile, "ms", "lower")] = scene[probe][quantile]
            samples = sorted(scene[probe]["samples"])
            if samples:
                result[(name + "/protocol", probe + "/p99", "ms", "lower")] = samples[math.ceil(len(samples) * .99) - 1]
        if scene["parameters"]["output_rate_bytes_s"]:
            result[(name + "/protocol", "output_bytes_s", "B/s", "higher")] = scene["output_bytes_s"]
        if scene.get("search"):
            result[(name + "/operation", "search_ready_ms", "ms", "lower")] = scene["search"]["complete_ms"]
        if scene.get("preview_ready_ms") is not None:
            result[(name + "/operation", "preview_ready_ms", "ms", "lower")] = scene["preview_ready_ms"]
    return result


def build_settings(path):
    info = Path(path).parent / "binary-build-info.txt"
    if not info.is_file():
        return None
    lines = info.read_text().splitlines()
    if not lines:
        raise ValueError("empty binary build metadata: " + str(info))
    result = {"go": lines[0].rsplit(": ", 1)[-1]}
    keys = {"-compiler", "-tags", "-race", "-trimpath", "CGO_ENABLED", "GOOS", "GOARCH", "GOARM64", "GOAMD64"}
    for line in lines[1:]:
        if line.strip().startswith("build\t"):
            key, _, value = line.strip()[6:].partition("=")
            if key in keys:
                result[key] = value
    return result


def compare_reports(before_paths, after_paths, split_scenarios=False):
    for paths in (before_paths, after_paths):
        if len({Path(p).resolve() for p in paths}) != len(paths):
            raise ValueError("duplicate measurement path cannot count as independent runs")
    groups = [[json.loads(Path(p).read_text()) for p in paths] for paths in (before_paths, after_paths)]
    if any(not g for g in groups):
        raise ValueError("provide application reports for both before and after")
    profiles = [build_settings(p) for paths in (before_paths, after_paths) for p in paths]
    if any(p != profiles[0] for p in profiles):
        raise ValueError("binary build conditions differ: Go version, architecture, tags or instrumentation")
    reference = conditions(groups[0][0])
    scene_conditions, scene_metric_keys = {}, {}
    common_reference = {k: v for k, v in reference.items() if k != "scenarios"}
    if split_scenarios and any(r.get("kind") in ("retty-api-benchmark", "retty-stream-benchmark") for g in groups for r in g):
        raise ValueError("split-scenarios applies only to native GUI measurements")
    metrics = []
    for reports in groups:
        if len({r["binary_sha256"] for r in reports}) != 1:
            raise ValueError("each side must measure one frozen binary, not mix versions")
        extracted = []
        for report in reports:
            if report.get("error"):
                raise ValueError("a failed measurement cannot be compared: " + report["error"])
            current = conditions(report)
            if split_scenarios:
                matching = {k: v for k, v in current.items() if k != "scenarios"} == common_reference
                for scene in report["scenarios"]:
                    name = scene["scenario"]
                    expected = scene_conditions.setdefault(name, current["scenarios"][name])
                    matching = matching and expected == current["scenarios"][name]
                    keys = report_metrics({"scenarios": [scene]}).keys()
                    matching = matching and scene_metric_keys.setdefault(name, keys) == keys
            else:
                matching = current == reference
            if report.get("version") != 1 or not matching:
                raise ValueError("measurement conditions differ: machine, workload, duration, geometry or focus")
            extracted.append(report_metrics(report))
        metrics.append(extracted)
    keys = set().union(*(m.keys() for m in metrics[0]))
    after_keys = set().union(*(m.keys() for m in metrics[1]))
    if keys != after_keys or (not split_scenarios and any(m.keys() != keys for group in metrics for m in group)):
        raise ValueError("application metric coverage differs")
    rows = [comparison(*key, [m[key] for m in metrics[0] if key in m],
                       [m[key] for m in metrics[1] if key in m]) for key in sorted(keys)]
    if split_scenarios:
        reference = {**common_reference, "scenarios": scene_conditions, "split_scenarios": True}
    sources = [[{"path": str(p), "binary_sha256": r["binary_sha256"], "measurement_checkout": r["git"],
                 "server_builds": r.get("server_builds"),
                 "binary_build_info": (Path(p).parent / "binary-build-info.txt").read_text()
                    if (Path(p).parent / "binary-build-info.txt").is_file() else None,
                 "system_load": r.get("system_load", {s["scenario"]: s["system_load"] for s in r.get("scenarios", [])})}
                for p, r in zip(paths, reports)] for paths, reports in zip((before_paths, after_paths), groups)]
    return rows, {**reference, "binary_build_settings": profiles[0]}, {"before": sources[0], "after": sources[1]}


def benchmarks(paths):
    if len({Path(p).resolve() for p in paths}) != len(paths):
        raise ValueError("duplicate benchmark path cannot count as additional samples")
    result, environments = {}, set()
    for path in paths:
        lines = Path(path).read_text().splitlines()
        if any(line.startswith(("FAIL", "--- FAIL", "--- SKIP")) for line in lines):
            raise ValueError("failed or skipped benchmark: " + str(path))
        environment = tuple(sorted({line for line in lines if line.startswith(("goos:", "goarch:", "cpu:"))}))
        if not all(any(line.startswith(key) for line in environment) for key in ("goos:", "goarch:", "cpu:")):
            raise ValueError("missing benchmark environment: " + str(path))
        environments.add(environment)
        package = ""
        for line in lines:
            if line.startswith("pkg:"):
                package = line.partition(":")[2].strip()
            fields = line.split()
            if not fields or not fields[0].startswith("Benchmark"):
                continue
            for unit in ("ns/op", "B/op", "allocs/op", "MB/s"):
                if unit in fields:
                    if not package:
                        raise ValueError("missing benchmark package identity")
                    key = ("benchmark/" + package + "/" + fields[0], unit, unit, "higher" if unit == "MB/s" else "lower")
                    result.setdefault(key, []).append(float(fields[fields.index(unit) - 1]))
    if not result or len(environments) != 1:
        raise ValueError("missing benchmarks or inconsistent benchmark environments")
    return result, environments.pop()


def compare_benchmarks(before_paths, after_paths):
    old, old_env = benchmarks(before_paths)
    new, new_env = benchmarks(after_paths)
    if old.keys() != new.keys() or old_env != new_env:
        raise ValueError("benchmark workloads, CPU parallelism or environments differ")
    return [comparison(*key, old[key], new[key]) for key in sorted(old)]


def number(value):
    return "—" if value is None else f"{value:,.3f}"


def render_markdown(report):
    lines = ["# Retty 性能前后对比", "", report["sample_note"], "",
             "每一侧为重复轮次的中位数；括号是最小值与最大值。百分比表示 after 相对 before 的变化。", "",
             "| 场景/接口 | 指标 | 单位 | Before | After | 变化 |",
             "| --- | --- | --- | ---: | ---: | ---: |"]
    for row in report["comparisons"]:
        def cell(group):
            return f"{number(group['median'])} ({number(group['min'])}–{number(group['max'])})"
        delta = number(row["delta_percent"]) + "%" if row["delta_percent"] is not None else "—"
        lines.append(f"| {row['scope']} | {row['metric']} | {row['unit']} | {cell(row['before'])} | {cell(row['after'])} | {delta} |")
    lines.extend(["", "## 测量限制", "", *["- " + s for s in report["limits"]], ""])
    return "\n".join(lines)


def render_html(report):
    data = json.dumps(report, ensure_ascii=False).replace("<", "\\u003c")
    return '''<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>Retty 性能前后对比</title><style>
body{font:15px system-ui;margin:32px;color:#17212b;background:#f5f7fa}h1{margin-bottom:8px}p{max-width:1000px;line-height:1.6}
input{padding:10px;width:min(600px,90%);margin:12px 0}table{border-collapse:collapse;width:100%;background:white}
th,td{padding:9px;text-align:right;border-bottom:1px solid #e4e9ef}th:first-child,td:first-child{text-align:left}
th{position:sticky;top:0;background:#edf1f5}td{font-variant-numeric:tabular-nums}small{display:block;color:#66717e}
.improved{color:#087542}.regressed{color:#b63636}footer{margin:24px 0;line-height:1.7}a{color:#185ebc}
</style><h1>Retty 性能前后对比</h1><p id="note"></p><input id="filter" placeholder="筛选场景、接口或指标" aria-label="筛选指标">
<table><thead><tr><th>场景/接口 · 指标</th><th>单位</th><th>Before 中位数</th><th>After 中位数</th><th>变化</th></tr></thead><tbody id="rows"></tbody></table>
<footer id="limits"></footer><script>const report=''' + data + ''';
const fmt=n=>n===null?'—':n.toLocaleString('zh-CN',{maximumFractionDigits:3});
document.querySelector('#note').textContent=report.sample_note;
function render(){const body=document.querySelector('#rows');body.replaceChildren();const q=document.querySelector('#filter').value.toLowerCase();
for(const r of report.comparisons){if(!(r.scope+' '+r.metric).toLowerCase().includes(q))continue;const tr=document.createElement('tr');
for(const text of [r.scope+' · '+r.metric,r.unit,fmt(r.before.median)+' ['+fmt(r.before.min)+'–'+fmt(r.before.max)+']',fmt(r.after.median)+' ['+fmt(r.after.min)+'–'+fmt(r.after.max)+']',fmt(r.delta_percent)+(r.delta_percent===null?'':'%')]){const td=document.createElement('td');td.textContent=text;tr.append(td)}
const benefit=r.delta*(r.direction==='higher'?1:-1);tr.lastChild.className=benefit>0?'improved':benefit<0?'regressed':'';body.append(tr)}}
for(const text of report.limits){const p=document.createElement('div');p.textContent='• '+text;document.querySelector('#limits').append(p)}
document.querySelector('#filter').addEventListener('input',render);render();</script></html>'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--before", nargs="+", type=Path, default=[])
    parser.add_argument("--after", nargs="+", type=Path, default=[])
    parser.add_argument("--before-bench", nargs="+", type=Path, default=[])
    parser.add_argument("--after-bench", nargs="+", type=Path, default=[])
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--split-scenarios", action="store_true",
                        help="combine independent per-scenario GUI runs, still requiring matching conditions and coverage")
    args = parser.parse_args()
    try:
        rows, conditions_used, sources = [], None, {}
        if args.before or args.after:
            rows, conditions_used, sources = compare_reports(args.before, args.after, args.split_scenarios)
        if args.before_bench or args.after_bench:
            rows.extend(compare_benchmarks(args.before_bench, args.after_bench))
        if not rows:
            raise ValueError("provide before/after application reports or benchmark files")
        preliminary = any(min(r["before"]["n"], r["after"]["n"]) < 3 for r in rows)
        report = {"version": 1, "preliminary": preliminary, "conditions": conditions_used, "sources": sources,
                  "comparisons": rows,
                  "sample_note": "初步结果：部分指标不足三轮，不能据此下整体性能结论。" if preliminary else
                                 "各指标至少三轮；请结合范围、系统负载和原始样本判断，不把中位数变化等同于统计显著。",
                  "limits": ["API 回显/快照延迟不包含 GPU 像素呈现时间；BenchmarkFrame 使用软件测试渲染器。",
                             "CPU 100% 表示一个核心。物理内存、RSS 和采样峰值分别比较。",
                             "每轮 P95 的中位数不等于合并所有请求后计算的 P95。",
                             "P99 按原始请求样本的 nearest-rank 计算；少量请求的尾部分位数只作调查线索。",
                             "Go 微基准的多轮重复不等同多个独立进程；完整应用每一轮使用新进程。",
                             "基准文件中的 CPU 并行度必须一致；应用报告的机器、工作负载、采样参数、网格与焦点必须匹配。",
                             "变化百分比以 before 为基数；before 为零时只报告绝对变化。颜色仅表示数值方向，不表示统计显著性。",
                             "加密流的接收端限速发生在解密后，不代表无线丢包或真实手机；仅比较相同直连/中继路径。"]}
        args.output.mkdir(parents=True, exist_ok=False)
        (args.output / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        (args.output / "report.md").write_text(render_markdown(report))
        (args.output / "report.html").write_text(render_html(report))
        print(json.dumps({"output": str(args.output), "metrics": len(rows), "preliminary": preliminary}))
    except (OSError, ValueError, KeyError) as error:
        parser.error(str(error))


if __name__ == "__main__":
    main()
