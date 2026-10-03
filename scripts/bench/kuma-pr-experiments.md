# Full Kuma PR experiments

These benchmarks run in GitHub Actions only. They use the exact
[Kuma PR #18941](https://github.com/kumahq/kuma/pull/18941) head
(`347bfd9022c6f9b074b0cd027e03233a429f32fd`), `./...`, tests enabled,
its `.golangci.yml` with 28 linters, Go 1.27.1, four Go CPUs, and separate
per-binary analysis and Go build caches. The runner is Ubuntu 24.04.
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

The first CI run also measured cold Go build caches, but it did not prewarm
the shared Go module cache before the first binary. Those paired single
samples are directionally useful, not a clean build-cache comparison:
upstream-first was 415.64 s upstream and 389.00 s golt; golt-first was
566.17 s golt and 633.72 s upstream. A follow-up CI run prewarms only the
module cache before repeating both orders.

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
3. Investigate analyzer allocation and `unparam` SSA traversal with targeted
   compatibility checks. `go/analysis` facts can flow between packages, so
   skipping dependency analysis without a proven equivalent loses findings.
4. Keep PGO or GC tuning only if their paired CI experiments improve the
   useful latency versus memory trade-off on this workload. In particular,
   golt's automatic half-physical-RAM `GOMEMLIMIT` is worth checking: the Go
   GC guide cautions against baking a memory limit into a CLI with variable
   inputs. The current data does not justify
   a Rust rewrite: the hot path is Go package loading, type checking, and
   Go analyzers, which a Rust front end would still need to call or replace.

The [Go GC guide](https://go.dev/doc/gc-guide) describes the CPU and memory
trade-off behind GOGC and GOMEMLIMIT. The
[Go PGO guide](https://go.dev/doc/pgo) requires representative profiles for
reliable gains. The [gopls implementation guide](https://go.dev/gopls/design/implementation)
shows a more ambitious route for edited-workspace performance: persistent
package metadata and granular invalidation. That design would be a larger
project than this list cache and needs correctness tests across edits, build
tags, generated files, and modules.
