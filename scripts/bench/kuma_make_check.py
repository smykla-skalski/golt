"""Compare the local Kuma make-check lint command on a CI runner."""

import argparse
import hashlib
import json
import os
import re
import shutil
import signal
import statistics
import subprocess
import time
from pathlib import Path

from local_workflow import tree_rss


MAX_RSS = 10 * 1024**3
TIMEOUT = 660
ISSUE_COUNTS = re.compile(r"Issues before processing: (\d+), after processing: (\d+)")


def git(workdir, *args):
    return subprocess.check_output(["git", *args], cwd=workdir)


def fingerprint(workdir):
    return (git(workdir, "diff", "--binary"),
            git(workdir, "status", "--porcelain", "--untracked-files=all"))


def run(binary, workdir, output, label, cache_dir):
    env = os.environ.copy()
    for key in ("GOGC", "GOMAXPROCS", "GOFLAGS", "GOLT_GC", "CI"):
        env.pop(key, None)
    env.update(CGO_ENABLED="0", GOMEMLIMIT="7GiB",
               GOLANGCI_LINT_CACHE=str(cache_dir))
    before = fingerprint(workdir)
    stdout_path = output / f"{label}.stdout"
    stderr_path = output / f"{label}.stderr"
    started = time.monotonic()
    peak_rss = 0
    with stdout_path.open("wb") as stdout, stderr_path.open("wb") as stderr:
        process = subprocess.Popen(
            [str(binary), "run", "--timeout=10m", "-v"], cwd=workdir,
            env=env, stdout=stdout, stderr=stderr, start_new_session=True,
        )
        try:
            while process.poll() is None:
                peak_rss = max(peak_rss, tree_rss(process.pid))
                if peak_rss > MAX_RSS:
                    raise MemoryError(f"{label} exceeded {MAX_RSS} bytes RSS")
                if time.monotonic() - started > TIMEOUT:
                    raise TimeoutError(f"{label} exceeded {TIMEOUT} seconds")
                time.sleep(0.1)
        except BaseException:
            os.killpg(process.pid, signal.SIGKILL)
            process.wait()
            raise
    if fingerprint(workdir) != before:
        raise RuntimeError(f"{label} modified the Kuma checkout")
    log = stderr_path.read_text(errors="replace")
    counts = ISSUE_COUNTS.search(log)
    if counts is None:
        raise RuntimeError(f"{label} did not report issue counts: {log[-2000:]}")
    return {
        "label": label,
        "seconds": round(time.monotonic() - started, 3),
        "peak_rss_mib": round(peak_rss / 1048576),
        "exit_code": process.returncode,
        "stdout_sha256": hashlib.sha256(stdout_path.read_bytes()).hexdigest(),
        "issues_before": int(counts.group(1)),
        "issues_after": int(counts.group(2)),
    }


def compare(first, second):
    fields = ("exit_code", "stdout_sha256", "issues_before", "issues_after")
    if any(first[field] != second[field] for field in fields):
        raise RuntimeError(f"diagnostics differ: {first} vs {second}")
    if first["exit_code"] not in (0, 1):
        raise RuntimeError(f"lint failed: {first}")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--upstream-bin", type=Path, required=True)
    parser.add_argument("--golt-bin", type=Path, required=True)
    parser.add_argument("--workdir", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    parser.add_argument("--order", choices=("upstream-first", "golt-first"), required=True)
    parser.add_argument("--baseline-label", default="Upstream")
    parser.add_argument("--runs", type=int, default=3)
    args = parser.parse_args()
    if args.runs < 1:
        parser.error("--runs must be positive")

    workdir = args.workdir.resolve()
    output = args.out.resolve()
    output.mkdir(parents=True, exist_ok=True)
    binaries = {"upstream": args.upstream_bin.resolve(), "golt": args.golt_bin.resolve()}
    names = ["upstream", "golt"] if args.order == "upstream-first" else ["golt", "upstream"]
    caches = {name: output / "cache" / name for name in binaries}
    source = workdir / "api/system/v1alpha1/datasource_helpers.go"
    original = source.read_bytes()
    if fingerprint(workdir) != (b"", b""):
        raise RuntimeError("Kuma checkout must be clean")

    records = []
    try:
        # Seed the shared Go build cache before measuring an empty lint cache.
        for name in names:
            record = run(binaries[name], workdir, output, f"seed-{name}", caches[name])
            if record["exit_code"] not in (0, 1):
                raise RuntimeError(f"seed failed: {record}")

        for case in ("cold-analysis", "warm-no-edit", "edited"):
            for iteration in range(args.runs):
                if case == "edited":
                    source.write_bytes(original +
                                       f"\n// make-check benchmark edit {iteration}\n".encode())
                pair = []
                for name in names:
                    if case == "cold-analysis":
                        shutil.rmtree(caches[name], ignore_errors=True)
                    label = f"{case}-{iteration}-{name}"
                    record = run(binaries[name], workdir, output, label, caches[name])
                    record.update(case=case, iteration=iteration, binary=name)
                    records.append(record)
                    pair.append(record)
                    print(json.dumps(record), flush=True)
                compare(*pair)
                if case == "edited":
                    source.write_bytes(original)
    finally:
        source.write_bytes(original)

    (output / "results.json").write_text(json.dumps(records, indent=2) + "\n")
    lines = [
        f"| Case | {args.baseline_label} | Golt | Golt change | {args.baseline_label} peak RSS | Golt peak RSS |",
        "| --- | ---: | ---: | ---: | ---: | ---: |",
    ]
    for case in ("cold-analysis", "warm-no-edit", "edited"):
        group = {name: [row for row in records if row["case"] == case and
                        row["binary"] == name] for name in binaries}
        wall = {name: statistics.median(row["seconds"] for row in rows)
                for name, rows in group.items()}
        rss = {name: max(row["peak_rss_mib"] for row in rows)
               for name, rows in group.items()}
        change = 100 * (wall["golt"] / wall["upstream"] - 1)
        lines.append(f"| {case} | {wall['upstream']:.2f} s | {wall['golt']:.2f} s | "
                     f"{change:+.1f}% | {rss['upstream']} MiB | {rss['golt']} MiB |")
    summary = "\n".join(lines) + "\n"
    (output / "summary.md").write_text(summary)
    print(summary)


if __name__ == "__main__":
    main()
