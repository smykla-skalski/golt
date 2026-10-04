# Upstream golangci-lint versus golt

For the full Kuma PR cold and warm single-run results, see
[the full Kuma PR experiments](kuma-pr-experiments.md).

## Full Kuma PR: two edited requests

The [full-workload CI run](https://github.com/smykla-skalski/golt/actions/runs/37186325743)
compares upstream v2.14.0 with merged golt main before the `unparam`
experiment. It uses the pinned Kuma PR #18941 head, `./...`, tests enabled,
all 28 configured linters, Go 1.27.1, two Go CPUs per process, `GOGC=80`, and
a 3 GiB Go soft memory limit per process. Two edited requests run serially or
at once; each order has three repetitions on a separate CI runner. The Go
build cache is shared and warm for timing samples; analysis caches are separate
per binary. RSS is the highest sampled aggregate process-tree RSS for a batch.

| Order | Policy | Upstream batch | Golt batch | Golt change | Upstream RSS | Golt RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Upstream first | Serial | 188.03 s | 20.97 s | 88.8% faster | 4,382 MiB | 2,253 MiB |
| Golt first | Serial | 210.41 s | 26.94 s | 87.2% faster | 4,371 MiB | 2,267 MiB |
| Upstream first | Parallel | 294.99 s | 32.67 s | 88.9% faster | 8,679 MiB | 4,923 MiB |
| Golt first | Parallel | 323.82 s | 34.33 s | 89.4% faster | 8,561 MiB | 4,929 MiB |

The configured workload reported 2,969 issues before filtering and zero final
issues for both binaries in every request. The CI artifact contains all
per-request logs and JSON output. Parallel batches were slower than serial
for both binaries, so heavy edited requests should remain serial. The Go
memory limit is soft; upstream's two processes exceeded 8 GiB aggregate RSS.

## Three-linter subset

**Compared:** [upstream v2.14.0](https://github.com/golangci/golangci-lint/releases/tag/v2.14.0)
(`114493f9`) and golt `10a6fe80` for the original four rows; the cold Kuma row
uses golt `58014932`. Both binaries were built from source with Go 1.26.0 and
tested on Ubuntu 24.04. All measurements use `govet`, `staticcheck`, and
`unused`. The parallel comparison below measures three overlapping linter
processes per binary; later sections cover single invocations and serial batches.

## Upstream parallel versus golt parallel

Both binaries ran three simultaneous requests with `--allow-parallel-runners`
on the same pinned workload. The table reports median batch completion time
from five CI repetitions and the highest sampled aggregate process-tree RSS.

| Workload | Upstream, 3 parallel | Golt, 3 parallel | Golt wall change | Upstream peak RSS | Golt peak RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| cache-buster, warm | 0.777 s | 0.286 s | **63.2% faster** | 380 MiB | 217 MiB |
| Kuma API, edited | 2.326 s | 1.726 s | **25.8% faster** | 1011 MiB | 931 MiB |

[CI run and raw per-request results](https://github.com/smykla-skalski/golt/actions/runs/37129315267).
This Kuma workload is only `./api/...` with tests disabled; it does not measure
the full-repository Kuma PR check. The serial comparison and protocol are below.

## Single invocation

Each row has seven samples per binary in each of two execution orders (14 per
binary). The table reports median wall time and median peak process-tree RSS.
Lower is better. Go build and module caches were prewarmed; “cold” means an
empty golangci-lint analysis cache. The edited case starts with a warm cache
and makes a new source edit before each sample. Kuma rows cover only
`./api/...` with tests disabled and three linters; they do not represent a
full-repository Kuma CI lint run.

| Workload | Cache | Upstream | Golt | Wall change | Upstream RSS | Golt RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| [cache-buster](https://github.com/smykla-skalski/golt/actions/runs/37110315118) | Cold | 3.950 s | 1.653 s | **58.2% faster** | 424 MiB | 689 MiB |
| [cache-buster](https://github.com/smykla-skalski/golt/actions/runs/37110315118) | Warm | 0.263 s | 0.078 s | **70.2% faster** | 123 MiB | 83 MiB |
| [Kuma API](https://github.com/smykla-skalski/golt/actions/runs/37127320813) | Cold | 8.954 s | 4.250 s | **52.5% faster** | 645 MiB | 799 MiB |
| [Kuma API](https://github.com/smykla-skalski/golt/actions/runs/37110950297) | Warm | 0.413 s | 0.112 s | **73.0% faster** | 125 MiB | 90 MiB |
| [Kuma API](https://github.com/smykla-skalski/golt/actions/runs/37110585540) | Edited | 1.958 s | 1.368 s | **30.1% faster** | 353 MiB | 370 MiB |

The cold small run traded speed for memory: golt’s median peak RSS was 62.4%
higher. Cold Kuma used 23.9% more RSS and 47.3% more analysis cache space.
On edited Kuma code, RSS was 4.9% higher and user CPU time was 38.2% lower.
The analysis cache was also larger for golt: 52.5% on cache-buster, 47.3% on
warm Kuma, and 35.2% on edited Kuma.

All four CI runs passed normalized JSON diagnostics comparison. The small workload
had no issues; the Kuma compatibility check found the same one issue and exit
code in both binaries. Timing improvements held in both execution orders.

This compares complete products at pinned commits. It does not attribute the
gain to a single fork change. Results cover one small repository and the
81-file Kuma API subset under two Go CPUs and a 1 GiB per-process RSS limit;
they do not predict performance on every codebase.

## Three-request developer workflow

[CI comparison](https://github.com/smykla-skalski/golt/actions/runs/37129315267):
upstream v2.14.0 versus golt `580682a3`, both built with Go 1.26.0 on Ubuntu
24.04. Each batch sends three requests for `govet`, `staticcheck`, and `unused`.
Serial batches launch one request at a time; parallel batches launch all three
with `--allow-parallel-runners`. Each process gets two Go CPUs. The small
workload has a warm no-edit cache; the Kuma API subset gets a unique source
edit before each paired batch. Each row has five batches per binary, with
alternating binary order and separate analysis caches. Completed diagnostics
matched across binaries.

| Workload | Policy | Upstream median batch | Golt median batch | Wall change | Upstream peak aggregate RSS | Golt peak aggregate RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| cache-buster, warm | Serial | 1.317 s | 0.558 s | **57.6% faster** | 127 MiB | 71 MiB |
| cache-buster, warm | Parallel | 0.777 s | 0.286 s | **63.2% faster** | 380 MiB | 217 MiB |
| Kuma API, edited | Serial | 1.923 s | 1.294 s | **32.7% faster** | 354 MiB | 330 MiB |
| Kuma API, edited | Parallel | 2.326 s | 1.726 s | **25.8% faster** | 1011 MiB | 931 MiB |

The RSS columns give the highest sampled aggregate process-tree RSS across
five batches; they are not per-process medians like the first table. For the
edited Kuma API subset, parallel requests increased golt's median batch time
from 1.294 to 1.726 seconds and peak aggregate RSS from 330 to 931 MiB.
The small warm workload benefited from overlap. These results still use the
Kuma API subset, not the full Kuma repository.

## Scope check against Kuma CI

[Kuma PR #18941](https://github.com/kumahq/kuma/pull/18941) ran from the
repository root with `.golangci.yml`, 28 linters, tests enabled, and Go 1.27.1.
The actual linter steps reported:

| Kuma PR job | Linter time | Package loading |
| --- | ---: | ---: |
| [Upstream v2.14.0](https://github.com/kumahq/kuma/actions/runs/36855468570/job/110361445645) | 367.9 s | 366.5 s |
| [Golt `bfb14a3`](https://github.com/kumahq/kuma/actions/runs/36855468570/job/110361444118) | 286.8 s | 286.6 s |

Both jobs missed the Go build cache while restoring their analysis caches.
They used separate runners and different binary builds and GC settings, so
these timings are an observation from that PR, not a controlled
upstream-versus-golt comparison.

The 4.250-second cold Kuma API number above excludes that expensive Go build
cache miss and analyzes a much smaller package set. “Cold” in the table refers
only to the golangci-lint analysis cache. Use the full-repository CI jobs to
estimate Kuma PR latency.

Source revisions: [cache-buster `1ed2641`](https://github.com/Automaat/cache-buster/commit/1ed2641a75cb424a592fa3eddf76da32180e7ed4)
and [Kuma `12fbf5f`](https://github.com/kumahq/kuma/commit/12fbf5f561e00b5f71eb5e72dc82e1a9eb4f9e87).
The CI runs link to raw timing and compatibility artifacts.
