"""Measure editor-like linter requests on a disposable CI runner."""

import argparse
import hashlib
import json
import os
import signal
import statistics
import subprocess
import time
from pathlib import Path

MAX_TOTAL_RSS = 3 * 1024**3


def tree_rss(root):
    parents = {}
    rss = {}
    for entry in Path("/proc").iterdir():
        if not entry.name.isdecimal():
            continue
        try:
            fields = (entry / "status").read_text().splitlines()
            data = dict(line.split(":", 1) for line in fields if ":" in line)
            pid = int(entry.name)
            parents[pid] = int(data["PPid"].strip())
            rss[pid] = int(data.get("VmRSS", "0 kB").split()[0]) * 1024
        except (OSError, KeyError, ValueError):
            continue
    descendants = {root}
    for _ in range(len(parents)):
        added = {pid for pid, parent in parents.items() if parent in descendants}
        if added <= descendants:
            break
        descendants |= added
    return sum(rss.get(pid, 0) for pid in descendants)


def start(binary, workdir, output, label, linters, concurrency, gc, daemon=False):
    env = os.environ.copy()
    env.update(
        {
            "GOMAXPROCS": str(concurrency),
            "GOFLAGS": f"-p={concurrency}",
            "GOMEMLIMIT": os.environ.get("GOLT_WORKFLOW_MEMORY_LIMIT", "512MiB"),
            "GOLT_GC": gc,
        }
    )
    if daemon:
        env["GOLT_DAEMON"] = "1"
        env["GOLT_DAEMON_IDLE"] = "20s"
    else:
        env.pop("GOLT_DAEMON", None)
    args = [
        str(binary),
        "run",
        "--no-config",
        "--default=none",
        "--enable-only=" + ",".join(linters),
        "--allow-parallel-runners",
        "--concurrency",
        str(concurrency),
        "--show-stats=false",
        "--output.json.path=stdout",
    ]
    if os.environ.get("GOLT_WORKFLOW_TESTS") == "false":
        args.append("--tests=false")
    args.append(os.environ.get("GOLT_WORKFLOW_PACKAGES", "./..."))
    stdout = (output / f"{label}.stdout").open("wb")
    stderr = (output / f"{label}.stderr").open("wb")
    process = subprocess.Popen(
        args, cwd=workdir, env=env, stdout=stdout, stderr=stderr, start_new_session=True
    )
    return {
        "process": process,
        "stdout": stdout,
        "stderr": stderr,
        "label": label,
        "started": time.monotonic(),
        "peak_rss": 0,
        "stdout_path": output / f"{label}.stdout",
        "stderr_path": output / f"{label}.stderr",
    }


def diagnostics_from_file(path):
    report = json.loads(path.read_bytes())
    return sorted(
        (
            issue["FromLinter"],
            issue["Pos"]["Filename"],
            issue["Pos"]["Line"],
            issue["Text"],
        )
        for issue in report["Issues"]
    )


def finish(task, cancelled=False):
    task["stdout"].close()
    task["stderr"].close()
    data = task["stdout_path"].read_bytes()
    try:
        diagnostics = diagnostics_from_file(task["stdout_path"])
        digest = hashlib.sha256(json.dumps(diagnostics).encode()).hexdigest()
    except (ValueError, KeyError, TypeError):
        digest = None
    result = {
        "label": task["label"],
        "seconds": round(time.monotonic() - task["started"], 3),
        "peak_rss_bytes": task["peak_rss"],
        "exit_code": task["process"].returncode,
        "cancelled": cancelled,
        "diagnostic_sha256": digest,
        "stdout_sha256": hashlib.sha256(data).hexdigest(),
    }
    if digest is None and not cancelled:
        result["stdout_preview"] = repr(data[:300])
        result["stderr_preview"] = repr(task["stderr_path"].read_bytes()[:300])
    return result


def run_batch(
    binary,
    workdir,
    output,
    label,
    requests,
    slots,
    concurrency,
    gc,
    daemon=False,
    supersede=False,
    linter_groups=None,
    timeout=90,
):
    pending = list(range(requests))
    active = []
    results = []
    started = time.monotonic()
    peak_total_rss = 0
    superseded = False
    while pending or active:
        while pending and len(active) < slots:
            index = pending.pop(0)
            active.append(
                start(
                    binary,
                    workdir,
                    output,
                    f"{label}-{index}",
                    linter_groups[index]
                    if linter_groups
                    else ["govet", "staticcheck", "unused"],
                    concurrency,
                    gc,
                    daemon,
                )
            )
            if supersede and index == 0:
                break
        if (
            supersede
            and not superseded
            and active
            and active[0]["label"].endswith("-0")
            and time.monotonic() - started >= 0.1
        ):
            os.killpg(active[0]["process"].pid, signal.SIGTERM)
            superseded = True
        total = 0
        for task in active:
            current = tree_rss(task["process"].pid)
            task["peak_rss"] = max(task["peak_rss"], current)
            total += current
        peak_total_rss = max(peak_total_rss, total)
        if total > MAX_TOTAL_RSS:
            for task in active:
                os.killpg(task["process"].pid, signal.SIGKILL)
                task["process"].wait()
                results.append(finish(task))
            raise MemoryError(f"{label} exceeded aggregate RSS ceiling: {total} bytes")
        for task in active[:]:
            if task["process"].poll() is not None:
                active.remove(task)
                results.append(
                    finish(
                        task, supersede and superseded and task["label"].endswith("-0")
                    )
                )
        if time.monotonic() - started > timeout:
            for task in active:
                os.killpg(task["process"].pid, signal.SIGKILL)
                task["process"].wait()
                results.append(finish(task))
            raise TimeoutError(f"{label} exceeded {timeout}s: {results}")
        time.sleep(0.05)
    return {
        "case": label,
        "makespan_seconds": round(time.monotonic() - started, 3),
        "peak_total_rss_bytes": peak_total_rss,
        "requests": results,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--workdir", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--repetitions", type=int, default=3)
    parser.add_argument("--profile", choices=["small", "large"], default="small")
    args = parser.parse_args()
    if args.repetitions < 1:
        parser.error("--repetitions must be positive")
    binary = args.binary.resolve()
    workdir = args.workdir.resolve()
    edit_path = (
        workdir / os.environ["GOLT_WORKFLOW_EDIT_FILE"]
        if args.profile == "large"
        else None
    )
    original_source = edit_path.read_bytes() if edit_path else None
    args.out.mkdir(parents=True, exist_ok=True)
    cases = [
        ("serial", 1, 2, "default", False, False),
        ("parallel", 3, 2, "default", False, False),
        ("bounded", 2, 1, "default", False, False),
        ("fork_gc", 2, 1, "fast", False, False),
        ("daemon", 1, 2, "default", True, False),
        ("supersede", 1, 2, "default", False, True),
    ]
    if args.profile == "large":
        cases = [
            case
            for case in cases
            if case[0] in {"serial", "parallel", "bounded", "fork_gc"}
        ]
    timeout = 300 if args.profile == "large" else 90
    records = []
    seed = run_batch(
        binary, workdir, args.out, "seed", 1, 1, 2, "default", timeout=timeout
    )
    if any(
        request["exit_code"] not in (0, 1) or request["diagnostic_sha256"] is None
        for request in seed["requests"]
    ):
        raise RuntimeError(f"warm-cache seed failed: {seed}")
    for repetition in range(args.repetitions):
        ordered = cases[repetition % len(cases) :] + cases[: repetition % len(cases)]
        for name, slots, concurrency, gc, daemon, supersede in ordered:
            label = f"{name}-r{repetition}"
            if edit_path:
                edit_path.write_bytes(
                    original_source + f"\n// golt CI edit {label}\n".encode()
                )
            result = run_batch(
                binary,
                workdir,
                args.out,
                label,
                3,
                slots,
                concurrency,
                gc,
                daemon,
                supersede,
                timeout=timeout,
            )
            result["repetition"] = repetition
            records.append(result)
            print(json.dumps(result), flush=True)
            for request in result["requests"]:
                if not request["cancelled"] and (
                    request["exit_code"] not in (0, 1)
                    or request["diagnostic_sha256"] is None
                ):
                    raise RuntimeError(f"linter request failed: {request}")
        if args.profile == "small":
            split = run_batch(
                binary,
                workdir,
                args.out,
                f"split-r{repetition}",
                3,
                3,
                1,
                "default",
                linter_groups=[["govet"], ["staticcheck"], ["unused"]],
                timeout=timeout,
            )
            split["repetition"] = repetition
            records.append(split)
            print(json.dumps(split), flush=True)
            for request in split["requests"]:
                if (
                    request["exit_code"] not in (0, 1)
                    or request["diagnostic_sha256"] is None
                ):
                    raise RuntimeError(f"split linter request failed: {request}")
    expected = seed["requests"][0]["diagnostic_sha256"]
    for row in records:
        if row["case"].startswith("split-"):
            continue
        for request in row["requests"]:
            if not request["cancelled"] and request["diagnostic_sha256"] != expected:
                raise RuntimeError(f"diagnostics changed in {row['case']}: {request}")
    if args.profile == "small":
        expected_diagnostics = diagnostics_from_file(args.out / "seed-0.stdout")
        for repetition in range(args.repetitions):
            combined = []
            for index in range(3):
                combined.extend(
                    diagnostics_from_file(
                        args.out / f"split-r{repetition}-{index}.stdout"
                    )
                )
            if sorted(combined) != expected_diagnostics:
                raise RuntimeError(
                    f"split linters changed diagnostics in repetition {repetition}"
                )
    (args.out / "results.json").write_text(json.dumps(records, indent=2) + "\n")
    print("\n| Case | Median makespan (s) | Max peak RSS (MiB) |")
    print("| --- | ---: | ---: |")
    names = [row[0] for row in cases]
    if args.profile == "small":
        names.append("split")
    for name in names:
        group = [row for row in records if row["case"].startswith(name + "-")]
        wall = statistics.median(row["makespan_seconds"] for row in group)
        rss = max(row["peak_total_rss_bytes"] for row in group) / 1048576
        print(f"| {name} | {wall:.2f} | {rss:.0f} |")


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(
            f"::error file=scripts/bench/local_workflow.py::benchmark failed: {type(exc).__name__}: {exc}",
            flush=True,
        )
        raise
