# Full Kuma PR experiments

These benchmarks run in GitHub Actions only. They use the exact
[Kuma PR #18941](https://github.com/kumahq/kuma/pull/18941) head
(`347bfd9022c6f9b074b0cd027e03233a429f32fd`), `./...`, tests enabled,
its `.golangci.yml` with 28 linters, Go 1.27.1, four Go CPUs, and separate
per-binary analysis and Go build caches. Timing runs set `GOGC=80` and
`GOMEMLIMIT=6144MiB` for both binaries. The runner is Ubuntu 24.04.
Upstream is golangci-lint v2.14.0; golt is built from this branch. The
[CI run](https://github.com/smykla-skalski/golt/actions/runs/37133788173)
contains the raw JSONL samples and logs.

## Upstream versus golt

The two orders run on separate CI runners. Each warm-Go row is a median of
three runs per binary, after a preparatory run has populated the Go build
cache. Cold analysis means an empty golangci-lint cache; warm means no source
edit; edited means a unique edit to a Go file after seeding the analysis cache.
RSS is median peak process-tree memory. Both binaries reported 2,969 issues
before configuration filtering and zero afterwards in the cold-analysis runs.

| Order | Case | Upstream | Golt | Golt change | Upstream RSS | Golt RSS |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| Upstream first | Cold analysis | 154.60 s | 85.74 s | 44.5% faster | 6,128 MiB | 3,430 MiB |
| Golt first | Cold analysis | 153.09 s | 81.03 s | 47.1% faster | 6,146 MiB | 3,316 MiB |
| Upstream first | Warm, no edit | 5.16 s | 0.42 s | 91.9% faster | 317 MiB | 128 MiB |
| Golt first | Warm, no edit | 4.46 s | 0.36 s | 91.9% faster | 316 MiB | 138 MiB |
| Upstream first | Edited | 83.21 s | 43.64 s | 47.6% faster | 6,095 MiB | 2,473 MiB |
| Golt first | Edited | 82.83 s | 40.38 s | 51.2% faster | 6,126 MiB | 2,527 MiB |

The follow-up CI run repeated the warm-Go comparison in both orders:
cold-analysis medians were 153.47–154.88 s upstream versus 79.29–82.30 s
golt; edited medians were 78.22–85.96 s versus 40.90–41.04 s; warm no-edit
medians were 4.56–5.01 s versus 0.38–0.40 s. The effect persisted.

The [follow-up CI run](https://github.com/smykla-skalski/golt/actions/runs/37137467998)
prewarmed the shared Go module cache, then gave each binary a separate empty
Go build and analysis cache. These are single samples per binary and order:

| Order | Upstream | Golt | Golt change | Upstream RSS | Golt RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| Upstream first | 621.57 s | 554.21 s | 10.8% faster | 6,154 MiB | 3,632 MiB |
| Golt first | 656.79 s | 558.85 s | 14.9% faster | 6,163 MiB | 3,493 MiB |

The first CI run did not prewarm the shared module cache before its cold-Go
samples, so it is not included in this table. The second run still shows
substantial between-run wall-time variation. On the warm-Go case, package
loading fell to roughly four seconds; on the upstream-first cold-Go case,
package loading took 7m51s upstream and 7m53s golt, while analyzer time was
2m30s versus 1m21s. Dependency builds dominated total time. The value of
restoring the Go build cache is larger than the
remaining golt-versus-upstream difference on this cache-miss workload.

## Pruning work after a dependency edit

The [cache-pruning CI run](https://github.com/smykla-skalski/golt/actions/runs/37145624122)
compares merged golt `main` with a candidate that caches package-scoped
diagnostics from fact-free analyzers by the package's own source and the
compiler export data of its direct imports. Analyzers that consume facts keep
the full dependency hash. The candidate also caches package-scoped issues
from gosec, revive, and unconvert. Both binaries use `GOGC=80`, a 6 GiB soft
memory limit, four Go CPUs, and separate Go build and analysis caches. The
table shows medians of three comment-only edited runs, after seeding the
analysis cache. RSS is median peak process-tree memory.

| Order | Merged main | Pruned candidate | Change | Main RSS | Candidate RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| Main first | 43.25 s | 22.07 s | 49.0% faster | 2,504 MiB | 2,251 MiB |
| Candidate first | 43.19 s | 21.93 s | 49.2% faster | 2,418 MiB | 2,218 MiB |

The same CI run compared the full Kuma JSON diagnostics before and after the
edit with warmed, separate caches. The candidate and main matched in both
cases. The first version, which excluded issue reporters, saved only 6.8–10.7%
on the edited run in a separate [CI experiment](https://github.com/smykla-skalski/golt/actions/runs/37143100560)
using the default GC policy. That established that reporter-backed linters
were the large remaining source of repeated work.

The [final CI run](https://github.com/smykla-skalski/golt/actions/runs/37147882166)
also measured three warm no-edit runs and repeated the edited case. The two
orders again used separate runners and caches. The warm difference was
0.01–0.04 seconds; the edited improvement held in both orders.

| Order | Case | Merged main | Pruned candidate | Candidate change |
| --- | --- | ---: | ---: | ---: |
| Candidate first | Warm, no edit | 0.29 s | 0.33 s | +0.04 s |
| Main first | Warm, no edit | 0.41 s | 0.42 s | +0.01 s |
| Candidate first | Edited | 31.98 s | 16.76 s | 47.6% faster |
| Main first | Edited | 42.59 s | 21.60 s | 49.3% faster |

## Isolating the list cache

This compares the **same golt binary** with `GOLT_LIST_CACHE=0` versus the
default. Medians of three samples; Go build cache warm.

| Case | List cache off | List cache on | Effect |
| --- | ---: | ---: | ---: |
| Cold analysis | 83.47 s | 82.89 s | 0.7% faster |
| Warm, no edit | 3.84 s | 0.42 s | 89.1% faster |
| Edited | 42.60 s | 42.97 s | 0.9% slower |

The debug log shows a warm cache hit in 72 ms. A source edit invalidates it,
and `go/packages.Load` then takes 3.57 s. The list cache solves repeated
no-edit requests; it does not explain the cold-analysis or edited gains.

## PGO build

The same golt commit was built with and without a CPU profile captured from
this Kuma workload. The table compares three samples per binary with a warm
Go build cache. This experiment uses `fork` for the PGO binary and `upstream`
for the ordinary golt binary in its raw results; it does not involve the
upstream golangci-lint release.

| Case | Ordinary golt | PGO golt | Change | Ordinary RSS | PGO RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| Cold analysis | 86.69 s | 84.29 s | 2.8% faster | 3,279 MiB | 3,543 MiB |
| Warm, no edit | 0.38 s | 0.38 s | unchanged | 132 MiB | 137 MiB |
| Edited | 42.99 s | 41.80 s | 2.8% faster | 2,485 MiB | 2,504 MiB |

This single profile and workload support further PGO testing, but not a
default release build. The cold-analysis memory cost is about 8%.

## GC policy for heavy concurrent work

The [follow-up CI run](https://github.com/smykla-skalski/golt/actions/runs/37137467998)
compared the same golt binary with its default policy (`GOGC=400` and its
automatic half-physical-memory soft limit) against Kuma's `GOGC=80` and the
benchmark's `GOMEMLIMIT=6144MiB`. Each row is a median of three cold-analysis
samples after warming the Go build cache; the two orders used separate
isolated caches. This tests the combined policies, not GOGC alone.

| Order | Golt default | `GOGC=80` | Default wall change | Default RSS | `GOGC=80` RSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| Default first | 58.12 s | 80.49 s | 27.8% faster | 7,596 MiB | 3,541 MiB |
| `GOGC=80` first | 62.04 s | 81.61 s | 24.0% faster | 7,669 MiB | 3,276 MiB |

The default saves about 20 seconds on one cold full-repository run but uses
more than twice the memory. For multiple heavy linter processes on one
developer machine, set `GOGC=80` and an appropriate `GOMEMLIMIT`, or use
golt's default serial queue. The 6 GiB soft limit used here is a measurement
setting, not a recommended value for every machine. The Go runtime's
[GC guide](https://go.dev/doc/gc-guide) cautions that an aggressive memory
limit can slow a CLI with variable inputs.

## Profile and likely next work

The unprofiled cold-analysis run with warm Go build cache took 84.5 s; its
loader took 3.89 s and analyzers about 80.6 s. With an empty Go build cache,
the preparatory run spent 8 minutes in package loading. CPU profiling of
the warm-Go run attributes about 30% of sampled CPU to runtime and GC work;
`ssautil.AllFunctions` contributes about 23% cumulative CPU, called via
`unparam`/CHA. Heap samples are dominated by `go/types` scopes and vars.
Profiles are an attribution aid, not timing samples.

1. Keep the list cache for editor and repeated CLI calls. It reduces the full
   Kuma no-edit request by about 3.4 s and changes no findings.
2. Preserve Go build cache availability in Kuma CI. Its actual PR jobs missed
   the build cache, and the current repository cache inventory has no
   `go-build-Linux-` entry. GitHub reports 10.6 GB active cache storage; its
   [documented default repository limit is 10 GB](https://docs.github.com/en/actions/reference/workflows-and-actions/dependency-caching).
   Eviction is a plausible cause, but historical cache inventory is absent.
   Check the configured limit and cache restore rate before changing Kuma CI.
   If the cache cannot fit within GitHub's quota, Go documents
   [`GOCACHEPROG`](https://go.dev/cmd/go/) for an externally managed build
   cache; measure restore cost before adopting one.
3. Prototype finer invalidation for edited packages. Both issue and fact
   caches currently use `HashModeNeedAllDeps`, which recursively hashes raw
   source. The Kuma edit benchmark only appends a comment to
   `api/system/v1alpha1/datasource_helpers.go`, but its edited run takes
   about 40 seconds. Reanalyze the changed package, then invalidate importers
   only when its exported type summary or relevant analyzer facts change.
   Test comment-only, function-body, exported-API, build-tag, and module edits
   against upstream diagnostics before treating any speedup as valid.
4. Keep the measured GC trade-off visible: `GOGC=80` cut RSS by more than
   half for overlapping full Kuma analyses; the default is faster for serial
   runs with available memory. Park PGO pending broader workloads.

## Rust rewrite status

The [Rust supervisor](../../rust/README.md) already exists and passes CI on
Linux and macOS. It is opt-in and owns timeout, cancellation, process-tree
cleanup, and RSS enforcement. A versioned Rust/Go worker protocol and an
opt-in transport also exist. Today that transport starts a fresh Go worker
for each invocation; the Go CLI remains the default and still runs package
loading, `go/types`, SSA, and all linters. It therefore does not remove the
measured 40–80 seconds of edited or cold analysis.

The [project roadmap](https://github.com/smykla-skalski/golt/issues/12)
deliberately keeps Go semantic analysis in Go. Rewriting the supervisor or
CLI front end more fully in Rust would leave the costly Go worker intact.
A full semantic rewrite would have to replace Go-native
[`go/packages`](https://pkg.go.dev/golang.org/x/tools/go/packages),
[`go/analysis`](https://pkg.go.dev/golang.org/x/tools/go/analysis), type
checking, facts, and the configured analyzers to preserve diagnostics.
No benchmark shows a gain that justifies that compatibility effort. Use the
existing Rust supervisor where its process controls help; put the next
performance experiment into cache invalidation and package analysis.

The [Go GC guide](https://go.dev/doc/gc-guide) describes the CPU and memory
trade-off behind GOGC and GOMEMLIMIT. The
[Go PGO guide](https://go.dev/doc/pgo) requires representative profiles for
reliable gains. The [gopls implementation guide](https://go.dev/gopls/design/implementation)
shows a more ambitious route for edited-workspace performance: persistent
package metadata and granular invalidation. That design would be a larger
project than this list cache and needs correctness tests across edits, build
tags, generated files, and modules.

The [Go build cache](https://go.dev/cmd/go/) is already safe for concurrent
`go` commands, so local workflows should share it. The
[gopls scalability write-up](https://go.dev/blog/gopls-scalability) reports
that persistent per-package summaries let separate processes reuse work;
golt already persists `go list` metadata, analyzer facts, and per-package
issues. A persistent typed summary and finer dependency invalidation are the
next architectural experiments, with a higher correctness and maintenance
cost than GC or PGO tuning.
