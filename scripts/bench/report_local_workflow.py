"""Expose CI benchmark results through check-run annotations."""

import json
import statistics
import sys
from pathlib import Path


def main():
    rows = json.loads(Path(sys.argv[1]).read_text())
    names = sorted({row["case"].split("-r")[0] for row in rows})
    for name in names:
        group = [row for row in rows if row["case"].startswith(name + "-r")]
        wall = statistics.median(row["makespan_seconds"] for row in group)
        rss = max(row["peak_total_rss_bytes"] for row in group) / 1048576
        cancelled = sum(
            request["cancelled"] for row in group for request in row["requests"]
        )
        message = f"median {wall:.2f}s; max aggregate RSS {rss:.0f} MiB"
        if name == "supersede":
            message += f"; stale requests cancelled {cancelled}/{len(group)}"
        print(
            f"::warning file=scripts/bench/readme.md,title=local-workflow/{name}::{message}"
        )


if __name__ == "__main__":
    main()
