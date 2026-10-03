"""Compare upstream and golt under the same overlapping request workload."""

import argparse
import json
import os
import statistics
from pathlib import Path

from local_workflow import run_batch


def checked_batch(binary, workdir, out, label, slots, cache_dir, timeout):
    result = run_batch(
        binary,
        workdir,
        out,
        label,
        requests=3,
        slots=slots,
        concurrency=2,
        gc="default",
        timeout=timeout,
        cache_dir=cache_dir,
    )
    for request in result["requests"]:
        if request["exit_code"] not in (0, 1) or request["diagnostic_sha256"] is None:
            raise RuntimeError(f"linter request failed: {request}")
    return result


def summarize(records):
    lines = [
        "| Policy | Upstream makespan | Golt makespan | Wall change | Upstream peak RSS | Golt peak RSS |",
        "| --- | ---: | ---: | ---: | ---: | ---: |",
    ]
    for policy in ("serial", "parallel"):
        groups = {
            name: [row for row in records if row["binary"] == name and row["policy"] == policy]
            for name in ("upstream", "golt")
        }
        wall = {
            name: statistics.median(row["makespan_seconds"] for row in group)
            for name, group in groups.items()
        }
        rss = {
            name: max(row["peak_total_rss_bytes"] for row in group) / 1048576
            for name, group in groups.items()
        }
        change = 100 * (wall["golt"] / wall["upstream"] - 1)
        lines.append(
            f"| {policy} | {wall['upstream']:.3f} s | {wall['golt']:.3f} s | "
            f"{change:+.1f}% | {rss['upstream']:.0f} MiB | {rss['golt']:.0f} MiB |"
        )
    return "\n".join(lines) + "\n"


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--upstream-bin", type=Path, required=True)
    parser.add_argument("--golt-bin", type=Path, required=True)
    parser.add_argument("--workdir", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--profile", choices=("small", "large"), required=True)
    parser.add_argument("--repetitions", type=int, default=5)
    args = parser.parse_args()
    if args.repetitions < 1:
        parser.error("--repetitions must be positive")

    binaries = {"upstream": args.upstream_bin.resolve(), "golt": args.golt_bin.resolve()}
    workdir = args.workdir.resolve()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    cache_dirs = {name: out / "cache" / name for name in binaries}
    edit_path = workdir / os.environ["GOLT_WORKFLOW_EDIT_FILE"] if args.profile == "large" else None
    original_source = edit_path.read_bytes() if edit_path else None
    timeout = 300 if edit_path else 90
    records = []

    try:
        expected = None
        for name, binary in binaries.items():
            seed = checked_batch(binary, workdir, out, f"{name}-seed", 1, cache_dirs[name], timeout)
            digest = seed["requests"][0]["diagnostic_sha256"]
            if expected is not None and digest != expected:
                raise RuntimeError("upstream and golt seed diagnostics differ")
            expected = digest

        for repetition in range(args.repetitions):
            for policy, slots in (("serial", 1), ("parallel", 3)):
                if edit_path:
                    edit_path.write_bytes(original_source + f"\n// comparison {policy} {repetition}\n".encode())
                order = list(binaries)
                if repetition % 2:
                    order.reverse()
                for name in order:
                    label = f"{name}-{policy}-r{repetition}"
                    result = checked_batch(
                        binaries[name], workdir, out, label, slots, cache_dirs[name], timeout
                    )
                    if any(request["diagnostic_sha256"] != expected for request in result["requests"]):
                        raise RuntimeError(f"diagnostics changed in {label}")
                    result.update(binary=name, policy=policy, repetition=repetition)
                    records.append(result)
                    print(json.dumps(result), flush=True)
    finally:
        if edit_path:
            edit_path.write_bytes(original_source)

    (out / "results.json").write_text(json.dumps(records, indent=2) + "\n")
    summary = summarize(records)
    (out / "summary.md").write_text(summary)
    print(summary)


if __name__ == "__main__":
    main()
