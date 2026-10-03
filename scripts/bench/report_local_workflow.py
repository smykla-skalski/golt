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
        low = min(row["makespan_seconds"] for row in group)
        high = max(row["makespan_seconds"] for row in group)
        rss = max(row["peak_total_rss_bytes"] for row in group) / 1048576
        cancelled = sum(
            request["cancelled"] for row in group for request in row["requests"]
        )
        message = f"median {wall:.2f}s (range {low:.2f}-{high:.2f}); max aggregate RSS {rss:.0f} MiB"
        if name in ("supersede", "native_supersede"):
            expected = len(group) * (2 if name == "native_supersede" else 1)
            message += f"; stale requests cancelled {cancelled}/{expected}"
        print(
            f"::warning file=scripts/bench/readme.md,title=local-workflow/{name}::{message}"
        )


if __name__ == "__main__":
    main()
