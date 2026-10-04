"""Compare unfiltered unparam diagnostics after representative Kuma edits."""

import argparse
import json
import os
import re
import shutil
import subprocess
from pathlib import Path


def run(binary, workdir, cache, output):
    env = os.environ.copy()
    env.update({
        "GOGC": "80",
        "GOMEMLIMIT": "3072MiB",
        "GOMAXPROCS": "2",
        "GOFLAGS": "-p=2",
        "GOLANGCI_LINT_CACHE": str(cache),
    })
    command = [
        str(binary), "run", "--no-config", "--default=none",
        "--enable-only=unparam", "--tests=true", "--concurrency=2",
        "--timeout=20m", "--issues-exit-code=0", "--show-stats=false",
        "--max-same-issues=0", "--max-issues-per-linter=0",
        "--output.json.path=stdout", "./...",
    ]
    completed = subprocess.run(command, cwd=workdir, env=env, capture_output=True,
                               timeout=1200, check=False)
    output.with_suffix(".stdout").write_bytes(completed.stdout)
    output.with_suffix(".stderr").write_bytes(completed.stderr)
    if completed.returncode:
        raise RuntimeError(f"{output.name}: exit {completed.returncode}")
    report = json.loads(completed.stdout)
    return sorted(json.dumps(issue, sort_keys=True) for issue in report["Issues"] or [])


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--main-bin", type=Path, required=True)
    parser.add_argument("--candidate-bin", type=Path, required=True)
    parser.add_argument("--workdir", type=Path, required=True)
    parser.add_argument("--out", type=Path, required=True)
    args = parser.parse_args()

    binaries = {"main": args.main_bin.resolve(), "candidate": args.candidate_bin.resolve()}
    workdir = args.workdir.resolve()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    source = workdir / "api/system/v1alpha1/datasource_helpers.go"
    module = workdir / "go.mod"
    tag_file = source.parent / "golt_bench_linux.go"
    fixture_file = source.parent / "golt_bench_unparam.go"
    original_source = source.read_bytes()
    original_module = module.read_bytes()
    if tag_file.exists() or fixture_file.exists():
        raise RuntimeError("benchmark file already exists")
    package = re.search(rb"(?m)^package (\w+)\s*$", original_source).group(1)
    body = re.search(rb"(?m)^func [^{]+\{\n", original_source)
    if body is None:
        raise RuntimeError("no function body found in edit source")
    edits = {
        "comment": (original_source + b"\n// golt benchmark comment\n", original_module, None),
        "body": (original_source[:body.end()] + b"\t_ = 0\n" +
                 original_source[body.end():], original_module, None),
        "exported": (original_source +
                     b"\nfunc GoltBenchExported() int { return 0 }\n", original_module, None),
        "build-tag": (original_source, original_module,
                      b"//go:build linux\n\npackage " + package +
                      b"\n\nfunc goltBenchLinux() {}\n"),
        "module": (original_source, original_module + b"\n// golt benchmark module edit\n", None),
    }
    results = []
    try:
        fixture_file.write_bytes(b"package " + package +
                                 b"\n\nfunc goltBenchUnusedParam(x int) {}\n")
        base_caches = {}
        seed = {}
        for name, binary in binaries.items():
            cache = out / "cache" / name
            cache.mkdir(parents=True)
            base_caches[name] = cache
            seed[name] = run(binary, workdir, cache, out / f"{name}-seed")
        if seed["main"] != seed["candidate"]:
            raise RuntimeError("seed diagnostics differ")
        if not seed["main"]:
            raise RuntimeError("unfiltered unparam seed has no diagnostics")

        for case, (source_bytes, module_bytes, tag_bytes) in edits.items():
            source.write_bytes(source_bytes)
            module.write_bytes(module_bytes)
            if tag_bytes is not None:
                tag_file.write_bytes(tag_bytes)
            issues = {}
            for name, binary in binaries.items():
                cache = out / "cache" / f"{name}-{case}"
                shutil.copytree(base_caches[name], cache)
                issues[name] = run(binary, workdir, cache, out / f"{name}-{case}")
            result = {"case": case, "main_issues": len(issues["main"]),
                      "candidate_issues": len(issues["candidate"]),
                      "match": issues["main"] == issues["candidate"]}
            results.append(result)
            (out / "results.json").write_text(json.dumps(results, indent=2) + "\n")
            print(json.dumps(result), flush=True)
            if not result["match"]:
                raise RuntimeError(f"{case}: diagnostics differ")
            source.write_bytes(original_source)
            module.write_bytes(original_module)
            tag_file.unlink(missing_ok=True)
    finally:
        source.write_bytes(original_source)
        module.write_bytes(original_module)
        tag_file.unlink(missing_ok=True)
        fixture_file.unlink(missing_ok=True)


if __name__ == "__main__":
    main()
