# Upstream golangci-lint versus golt

## Kuma local `make check` lint step

The [CI comparison](https://github.com/smykla-skalski/golt/actions/runs/37224150227)
replays the Go lint recipe from Kuma's local `make check` on pinned Kuma
`c993a123`: `CGO_ENABLED=0 GOMEMLIMIT=7GiB golangci-lint run --timeout=10m -v`.
It uses upstream v2.14.0 and golt `961b135e`, Go 1.27.1, the root config's
28 linters, tests enabled, and no benchmark-imposed CPU cap or `GOGC` value.
Each row is the median of three single-invocation samples per binary. The
two binary orders ran on separate Ubuntu 24.04 runners. RSS is the highest
sampled process-tree RSS in the three runs. A preliminary run warmed the shared
Go build cache; each binary used a separate lint cache. Cold analysis clears
that lint cache, warm no-edit repeats the clean source, and edited appends a
new comment to a Go file after seeding the cache.

| Order | Case | Upstream | Golt | Golt change | Upstream RSS | Golt RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Golt first | Cold analysis | 78.82 s | 30.96 s | 60.7% faster | 7,185 MiB | 6,905 MiB |
| Upstream first | Cold analysis | 101.60 s | 40.35 s | 60.3% faster | 7,163 MiB | 6,825 MiB |
| Golt first | Warm, no edit | 3.13 s | 0.36 s | 88.6% faster | 360 MiB | 166 MiB |
| Upstream first | Warm, no edit | 4.04 s | 0.39 s | 90.4% faster | 351 MiB | 151 MiB |
| Golt first | Edited | 42.91 s | 8.57 s | 80.0% faster | 7,050 MiB | 5,286 MiB |
| Upstream first | Edited | 56.02 s | 11.18 s | 80.0% faster | 7,086 MiB | 5,123 MiB |

All 36 timed requests exited successfully with identical output and 2,969
issues before filtering, zero afterwards. No request modified the checkout,
despite Kuma's `issues.fix: true` setting. The full `make check` also runs
format generators and other lint targets; this table measures its Go lint
recipe only. Kuma's CI lint action differs: it passes `--fix=false` and sets
`GOGC=80`. The command is exact, but timings are from CI runners rather than
a developer's Mac. The two orders show runner-dependent absolute times;
the relative edited improvement was 80.0% in both.

For the edited golt sample, verbose logs report 2.2 seconds in package loading
and 6.1 seconds in linters; `unparam` accounts for about 5.0 seconds of summed
analyzer time. Further edited-run work should measure factful analyzer cache
reuse and profile the remaining analysis before changing the default GC policy.

For the full Kuma PR cold and warm single-run results, see
[the full Kuma PR experiments](kuma-pr-experiments.md).

### Reusing source SSA functions in `unparam`

The [profile](https://github.com/smykla-skalski/golt/actions/runs/37227153985)
of an edited local lint run took 9.12 seconds. `unparam` used 4.46 seconds of
CPU samples, including about 4.0 seconds in SSA function discovery and runtime
type traversal. The [paired CI run](https://github.com/smykla-skalski/golt/actions/runs/37228071499)
compares golt main `3008e7e6` with candidate `7bea8cbb` on the same pinned
Kuma checkout and command above. Each row is the median of three requests;
orders ran on separate Ubuntu 24.04 runners.

| Order | Case | Main | Candidate | Change | Main RSS | Candidate RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Candidate first | Cold analysis | 41.53 s | 40.01 s | 3.6% faster | 6,889 MiB | 6,864 MiB |
| Main first | Cold analysis | 41.02 s | 38.49 s | 6.2% faster | 6,895 MiB | 6,883 MiB |
| Candidate first | Warm, no edit | 0.38 s | 0.38 s | unchanged | 144 MiB | 152 MiB |
| Main first | Warm, no edit | 0.38 s | 0.37 s | unchanged | 152 MiB | 150 MiB |
| Candidate first | Edited | 11.31 s | 9.88 s | 12.6% faster | 4,949 MiB | 4,784 MiB |
| Main first | Edited | 10.77 s | 9.22 s | 14.4% faster | 5,137 MiB | 4,594 MiB |

All 36 timed requests produced identical output, 2,969 prefilter issues and
zero final issues. The separate unfiltered `unparam` parity check matched main
for comment, body, exported-function, build-tag, and module edits, with a
deliberate unused-parameter finding in each case. This is an incremental golt
comparison, not a new upstream comparison. The profile uses sampled CPU time,
which can exceed wall time when analysis runs concurrently.

That first candidate skipped reflection type traversal for every package. It
can miss wrappers used by reflective method calls. The revised candidate
`fa8b596e` keeps the full traversal whenever a package declares local methods
and uses the source-SSA path for packages without local methods. The
[final paired CI run](https://github.com/smykla-skalski/golt/actions/runs/37229587519)
measured the revised candidate against the same main commit and command.

| Order | Case | Main | Revised candidate | Change | Main RSS | Candidate RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Candidate first | Cold analysis | 38.16 s | 37.33 s | 2.2% faster | 6,926 MiB | 6,920 MiB |
| Main first | Cold analysis | 30.13 s | 29.52 s | 2.0% faster | 6,900 MiB | 6,841 MiB |
| Candidate first | Warm, no edit | 0.35 s | 0.35 s | unchanged | 151 MiB | 153 MiB |
| Main first | Warm, no edit | 0.35 s | 0.35 s | unchanged | 170 MiB | 171 MiB |
| Candidate first | Edited | 10.20 s | 9.54 s | 6.4% faster | 4,986 MiB | 4,732 MiB |
| Main first | Edited | 8.41 s | 7.54 s | 10.3% faster | 5,047 MiB | 4,282 MiB |

All 36 timed requests again produced identical output and issue counts. The
unfiltered `unparam` parity check matched main across the same five edit
types. The revised candidate is the result to use when estimating the change
to local `make check` lint time.

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
per-request logs and JSON output. For this two-request setup, serial batches
were faster and used less memory for both binaries. The Go
memory limit is soft; upstream's two processes exceeded 8 GiB aggregate RSS.

The [optimized-candidate CI run](https://github.com/smykla-skalski/golt/actions/runs/37188867865)
repeated the same workload after narrowing `unparam`'s SSA function walk.
Each row again has three batches per binary and execution order.

| Order | Policy | Upstream batch | Optimized golt batch | Golt change | Upstream RSS | Golt RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Golt first | Serial | 156.68 s | 15.50 s | 90.1% faster | 4,413 MiB | 2,198 MiB |
| Upstream first | Serial | 188.81 s | 16.78 s | 91.1% faster | 4,410 MiB | 2,178 MiB |
| Golt first | Parallel | 242.24 s | 19.07 s | 92.1% faster | 8,683 MiB | 4,638 MiB |
| Upstream first | Parallel | 307.57 s | 25.16 s | 91.8% faster | 8,601 MiB | 4,537 MiB |

The 2,969 prefilter issue count and zero final issues matched in every
request, as did the final JSON hash. Since the configured run filters every
issue, [separate unfiltered `unparam` edit checks](kuma-pr-experiments.md#scoping-unparams-ssa-function-walk)
also compared diagnostics across five edit types with a deliberate finding.

These are complete-product comparisons. Run-to-run variation means the two
tables do not isolate the SSA change. The paired main-versus-candidate edit
benchmark found the candidate 20.4–25.1% faster with lower RSS; see
[the Kuma experiments](kuma-pr-experiments.md#scoping-unparams-ssa-function-walk).

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
only to the golangci-lint analysis cache. Use the controlled full Kuma tables
above for edited local requests; the Kuma PR jobs show the cache-miss CI case.

Three-linter subset source revisions: [cache-buster `1ed2641`](https://github.com/Automaat/cache-buster/commit/1ed2641a75cb424a592fa3eddf76da32180e7ed4)
and [Kuma `12fbf5f`](https://github.com/kumahq/kuma/commit/12fbf5f561e00b5f71eb5e72dc82e1a9eb4f9e87).
The CI runs link to raw timing and compatibility artifacts.
