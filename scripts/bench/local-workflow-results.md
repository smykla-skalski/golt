# Developer workflow benchmark

CI run: [five-repetition experiment](https://github.com/smykla-skalski/golt/actions/runs/37107987965).
All completed requests returned the same normalized diagnostics. No linter
benchmark ran on a developer machine.

The workload sends three requests for `govet,staticcheck,unused`. The small
project is a warm no-edit cache-buster checkout (three repetitions). The larger
project is a pinned Kuma API checkout; each batch makes a unique source edit
before its three requests (five repetitions). Runs use Ubuntu 24.04 and Go
1.26. The table shows median batch makespan and the largest sampled aggregate
process-tree RSS across repetitions.

| Policy | Warm no edit | Warm peak RSS | Kuma edit | Kuma peak RSS |
| --- | ---: | ---: | ---: | ---: |
| Serial, Go default GC | 0.56 s | 113 MiB | 1.91 s | 323 MiB |
| Serial, golt GC policy | 0.56 s | 69 MiB | 1.77 s | 350 MiB |
| Three parallel, two CPUs each | 0.30 s | 221 MiB | 2.81 s | 967 MiB |
| Two parallel, one CPU each | 0.41 s | 261 MiB | 2.66 s | 640 MiB |
| Bounded, golt GC policy | 0.41 s | 141 MiB | 2.40 s | 738 MiB |
| Serial daemon | 0.57 s | 131 MiB* | 1.97 s | 389 MiB* |
| Cancel first stale request | 0.56 s | 71 MiB | 1.92 s | 327 MiB |
| Three separate linter processes | 0.28 s | 583 MiB | — | — |

*Daemon RSS excludes its detached server, so it cannot be compared with other
RSS values. The cancellation case returns two completed responses and cancels
one request; all five Kuma cancellations succeeded. The split-linter small case
had one 4.62-second outlier; its median was 0.28 seconds. Kuma tests were
disabled, and its runs used a 768 MiB Go soft memory limit per process; small
runs used 512 MiB. All cases had a 3 GiB aggregate RSS safety ceiling.

## Decision

- Keep a single combined linter invocation. Splitting linters gave no warm
  latency benefit and multiplied memory use.
- Keep serial execution with golt's GC policy for edited, analysis-heavy
  requests. Against that policy, three unrestricted parallel runs were 59%
  slower and reached 2.8 times the aggregate RSS. The GC policy itself made
  serial Kuma runs 7% faster at an 8% RSS cost.
- Permit overlapping warm no-op requests only when latency matters; it halved
  batch makespan on the small project while nearly doubling RSS.
- Do not make bounded overlap or daemon reuse the default based on this data.
  Neither beat serial Kuma edits. A shared admission policy would need to
  allow cheap no-op requests while serializing edited analysis.
- Process cancellation works for stale requests. Internal analyzer-context
  propagation remains untested; this experiment terminated whole processes.

These results describe one small checkout and one Kuma API subset on a hosted
runner. They do not establish a machine-independent threshold for switching
between serial and parallel modes. The harness and raw per-request results are
in the CI artifact.
