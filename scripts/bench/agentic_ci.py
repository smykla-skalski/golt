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


def request(binary, output, label, name, workdir, cache, gc="default", gogc=None,
            full_kuma=False):
    is_kuma = name.startswith("kuma")
    extra_env = {"GOGC": gogc} if gogc else {"GOGC": ""}
    return start(
        binary, workdir, output, label, LINTERS, 2, gc,
        cache_dir=cache, packages="./..." if full_kuma or not is_kuma else "./api/...",
        tests=full_kuma or not is_kuma,
        memory_limit="3GiB" if full_kuma else ("768MiB" if is_kuma else "512MiB"),
        extra_env=extra_env,
    )


def run_policy(binary, output, label, workdirs, cache, weights, capacity, gc="default",
               gogc=None, full_kuma=False):
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
                task = request(binary, output, f"{label}-{name}", name, workdirs[name],
                               cache, gc, gogc, full_kuma)
                active.append((name, task))
                used += weights[name]
            total = 0
            for _, task in active:
                rss = tree_rss(task["process"].pid)
                task["peak_rss"] = max(task["peak_rss"], rss)
                total += rss
            peak = max(peak, total)
            if total > (10 * 1024**3 if full_kuma else MAX_RSS):
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
            timeout = 1200 if full_kuma else 300
            if time.monotonic() - started > timeout:
                raise TimeoutError(f"{label} exceeded {timeout} seconds")
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


def run_full_kuma(binary, output, workdirs, originals, cache, repetitions):
    heavy = {name: workdirs[name] for name in originals}
    expected = {}
    counts = {}
    records = []
    for name, workdir in heavy.items():
        seed = run_policy(binary, output, f"full-seed-{name}", {name: workdir},
                          cache, {name: 1}, 1, gogc="80", full_kuma=True)
        result = seed["requests"][name]
        expected[name] = result["diagnostic_sha256"]
        counts[name] = result.get("issue_counts")
        if counts[name] is None:
            raise RuntimeError(f"full Kuma issue counts missing for {name}")
        records.append(seed)
    policies = (("full-serial", 1), ("full-parallel", 2))
    for repetition in range(repetitions):
        for policy, capacity in policies[repetition % 2:] + policies[:repetition % 2]:
            for path, original in originals.values():
                path.write_bytes(original + f"\n// agentic CI {policy} {repetition}\n".encode())
            row = run_policy(binary, output, f"{policy}-r{repetition}", heavy,
                             cache, {name: 1 for name in heavy}, capacity,
                             gogc="80", full_kuma=True)
            check_diagnostics(row, expected)
            for name, result in row["requests"].items():
                if result.get("issue_counts") != counts[name]:
                    raise RuntimeError(f"{row['case']}: issue counts changed for {name}")
            records.append(row)
            print(json.dumps(row), flush=True)
    return records


def summarize(output, records, prefixes):
    (output / "results.json").write_text(json.dumps(records, indent=2) + "\n")
    lines = ["| Case | Median batch (s) | Peak aggregate RSS (MiB) |",
             "| --- | ---: | ---: |"]
    for prefix in prefixes:
        group = [row for row in records if row["case"] == prefix or row["case"].startswith(prefix + "-r")]
        median = statistics.median(row["seconds"] for row in group)
        peak = max(row["peak_rss_mib"] for row in group)
        lines.append(f"| {prefix} | {median:.2f} | {peak} |")
        print(f"::notice file=scripts/bench/agentic_ci.py,title=agentic/{prefix}::"
              f"median {median:.2f}s (range {min(row['seconds'] for row in group):.2f}-"
              f"{max(row['seconds'] for row in group):.2f}); max aggregate RSS {peak} MiB")
    summary = "\n".join(lines) + "\n"
    (output / "summary.md").write_text(summary)
    print(summary)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--small", type=Path)
    parser.add_argument("--small-copy", type=Path)
    parser.add_argument("--kuma", type=Path, required=True)
    parser.add_argument("--kuma-copy", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--repetitions", type=int, default=2)
    parser.add_argument("--full-kuma", action="store_true")
    args = parser.parse_args()
    if args.repetitions < 1:
        parser.error("--repetitions must be positive")
    if not args.full_kuma and (args.small is None or args.small_copy is None):
        parser.error("--small and --small-copy are required outside full Kuma mode")
    binary = args.binary.resolve()
    output = args.out.resolve()
    output.mkdir(parents=True, exist_ok=True)
    workdirs = {
        "kuma-a": args.kuma.resolve(),
        "kuma-b": args.kuma_copy.resolve(),
    }
    if not args.full_kuma:
        workdirs = {
            "small-a": args.small.resolve(),
            "small-b": args.small_copy.resolve(),
            **workdirs,
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
        if args.full_kuma:
            records = run_full_kuma(binary, output, workdirs, originals, cache,
                                    args.repetitions)
            summarize(output, records, ("full-serial", "full-parallel"))
            return
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

    summarize(output, records, ("serial", "parallel", "two-slots", "weighted",
                                "gc-go-default", "gc-gogc-80", "gc-golt-fast",
                                "worktree-shared", "worktree-isolated"))


if __name__ == "__main__":
    main()
