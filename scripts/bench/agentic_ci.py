"""Compare multi-checkout lint policies on a disposable CI runner."""

import argparse
import json
import os
import signal
import statistics
import time
from pathlib import Path

from local_workflow import finish, start, tree_rss

MIB = 1024**2
MAX_RSS = 3 * 1024**3
LINTERS = ["govet", "staticcheck", "unused"]


def request(binary, output, label, name, workdir, cache, gc="default", gogc=None):
    is_kuma = name.startswith("kuma")
    extra_env = {"GOGC": gogc} if gogc else {"GOGC": ""}
    return start(
        binary, workdir, output, label, LINTERS, 2, gc,
        cache_dir=cache, packages="./api/..." if is_kuma else "./...",
        tests=not is_kuma, memory_limit="768MiB" if is_kuma else "512MiB",
        extra_env=extra_env,
    )


def run_policy(binary, output, label, workdirs, cache, weights, capacity, gc="default", gogc=None):
    pending = list(workdirs)
    active = []
    results = {}
    started = time.monotonic()
    peak = 0
    completed = []
    try:
        while pending or active:
            used = sum(weights[name] for name, _ in active)
            for name in pending[:]:
                if used + weights[name] > capacity:
                    continue
                pending.remove(name)
                task = request(binary, output, f"{label}-{name}", name, workdirs[name], cache, gc, gogc)
                active.append((name, task))
                used += weights[name]
            total = 0
            for _, task in active:
                rss = tree_rss(task["process"].pid)
                task["peak_rss"] = max(task["peak_rss"], rss)
                total += rss
            peak = max(peak, total)
            if total > MAX_RSS:
                raise MemoryError(f"{label} exceeded aggregate RSS ceiling: {total}")
            for name, task in active[:]:
                if task["process"].poll() is None:
                    continue
                active.remove((name, task))
                result = finish(task)
                completed.append(name)
                if result["exit_code"] not in (0, 1) or result["diagnostic_sha256"] is None:
                    raise RuntimeError(f"{label}: {result}")
                results[name] = result
            if time.monotonic() - started > 300:
                raise TimeoutError(f"{label} exceeded 300 seconds")
            time.sleep(0.05)
    finally:
        for _, task in active:
            if task["process"].poll() is None:
                os.killpg(task["process"].pid, signal.SIGKILL)
                task["process"].wait()
            task["stdout"].close()
            task["stderr"].close()
    return {
        "case": label,
        "seconds": round(time.monotonic() - started, 3),
        "peak_rss_mib": round(peak / MIB),
        "completion_order": completed,
        "requests": results,
    }


def check_diagnostics(row, expected):
    for name, result in row["requests"].items():
        if result["diagnostic_sha256"] != expected[name]:
            raise RuntimeError(f"{row['case']}: diagnostics changed for {name}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--small", type=Path, required=True)
    parser.add_argument("--small-copy", type=Path, required=True)
    parser.add_argument("--kuma", type=Path, required=True)
    parser.add_argument("--kuma-copy", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--repetitions", type=int, default=2)
    args = parser.parse_args()
    if args.repetitions < 1:
        parser.error("--repetitions must be positive")
    binary = args.binary.resolve()
    output = args.out.resolve()
    output.mkdir(parents=True, exist_ok=True)
    workdirs = {
        "small-a": args.small.resolve(),
        "small-b": args.small_copy.resolve(),
        "kuma-a": args.kuma.resolve(),
        "kuma-b": args.kuma_copy.resolve(),
    }
    cache = output / "shared-cache"
    cache.mkdir()
    originals = {}
    for name in ("kuma-a", "kuma-b"):
        path = workdirs[name] / "api/system/v1alpha1/datasource_helpers.go"
        originals[name] = (path, path.read_bytes())
    records = []
    expected = {}
    try:
        for name, workdir in workdirs.items():
            seed = run_policy(binary, output, f"seed-{name}", {name: workdir}, cache, {name: 1}, 1)
            expected[name] = seed["requests"][name]["diagnostic_sha256"]
            records.append(seed)

        policies = (
            ("serial", {name: 1 for name in workdirs}, 1),
            ("parallel", {name: 1 for name in workdirs}, 4),
            ("two-slots", {name: 1 for name in workdirs}, 2),
            ("weighted", {name: (2 if name.startswith("kuma") else 1) for name in workdirs}, 3),
        )
        for repetition in range(args.repetitions):
            for policy, weights, capacity in policies[repetition % 4:] + policies[:repetition % 4]:
                for name, (path, original) in originals.items():
                    path.write_bytes(original + f"\n// agentic CI {policy} {repetition}\n".encode())
                row = run_policy(binary, output, f"{policy}-r{repetition}", workdirs, cache, weights, capacity)
                check_diagnostics(row, expected)
                records.append(row)
                print(json.dumps(row), flush=True)

        heavy = {name: workdirs[name] for name in originals}
        for gc, gogc in (("go-default", None), ("gogc-80", "80"), ("golt-fast", None)):
            for name, (path, original) in originals.items():
                path.write_bytes(original + f"\n// agentic CI gc {gc}\n".encode())
            row = run_policy(binary, output, f"gc-{gc}", heavy, cache,
                             {name: 1 for name in heavy}, 2,
                             gc="fast" if gc == "golt-fast" else "default", gogc=gogc)
            check_diagnostics(row, expected)
            records.append(row)
            print(json.dumps(row), flush=True)

        for name, (path, original) in originals.items():
            path.write_bytes(original)
        for repetition in range(args.repetitions):
            cross_cache = output / f"cross-worktree-cache-{repetition}"
            cross_cache.mkdir()
            seed = run_policy(binary, output, f"worktree-seed-r{repetition}",
                              {"kuma-a": workdirs["kuma-a"]}, cross_cache,
                              {"kuma-a": 1}, 1)
            check_diagnostics(seed, expected)
            records.append(seed)
            isolated = output / f"isolated-cache-{repetition}"
            isolated.mkdir()
            order = (("shared", cross_cache), ("isolated", isolated))
            if repetition % 2:
                order = order[::-1]
            for cache_name, cache_dir in order:
                row = run_policy(binary, output, f"worktree-{cache_name}-r{repetition}",
                                 {"kuma-b": workdirs["kuma-b"]}, cache_dir,
                                 {"kuma-b": 1}, 1)
                check_diagnostics(row, expected)
                records.append(row)
                print(json.dumps(row), flush=True)
    finally:
        for path, original in originals.values():
            path.write_bytes(original)

    (output / "results.json").write_text(json.dumps(records, indent=2) + "\n")
    lines = ["| Case | Median batch (s) | Peak aggregate RSS (MiB) |",
             "| --- | ---: | ---: |"]
    for prefix in ("serial", "parallel", "two-slots", "weighted", "gc-go-default",
                   "gc-gogc-80", "gc-golt-fast", "worktree-shared", "worktree-isolated"):
        group = [row for row in records if row["case"] == prefix or row["case"].startswith(prefix + "-r")]
        lines.append(f"| {prefix} | {statistics.median(row['seconds'] for row in group):.2f} | "
                     f"{max(row['peak_rss_mib'] for row in group)} |")
    summary = "\n".join(lines) + "\n"
    (output / "summary.md").write_text(summary)
    print(summary)


if __name__ == "__main__":
    main()
