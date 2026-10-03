"""Publish CI benchmark numbers as public commit statuses."""

import json
import os
import statistics
import sys
import urllib.request
from pathlib import Path


def main():
    rows = json.loads(Path(sys.argv[1]).read_text())
    names = sorted({row["case"].split("-r")[0] for row in rows})
    repository = os.environ["GITHUB_REPOSITORY"]
    sha = os.environ["GITHUB_SHA"]
    token = os.environ["GITHUB_TOKEN"]
    for name in names:
        group = [row for row in rows if row["case"].startswith(name + "-r")]
        wall = statistics.median(row["makespan_seconds"] for row in group)
        rss = max(row["peak_total_rss_bytes"] for row in group) / 1048576
        payload = json.dumps(
            {
                "state": "success",
                "context": f"local-workflow/{name}",
                "description": f"median {wall:.2f}s; max aggregate RSS {rss:.0f} MiB",
                "target_url": f"https://github.com/{repository}/actions/runs/{os.environ['GITHUB_RUN_ID']}",
            }
        ).encode()
        request = urllib.request.Request(
            f"https://api.github.com/repos/{repository}/statuses/{sha}",
            data=payload,
            headers={
                "Authorization": f"Bearer {token}",
                "Accept": "application/vnd.github+json",
                "Content-Type": "application/json",
            },
            method="POST",
        )
        with urllib.request.urlopen(request, timeout=15) as response:
            if response.status != 201:
                raise RuntimeError(f"status publish failed: {response.status}")


if __name__ == "__main__":
    main()
