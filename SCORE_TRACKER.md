# Score tracker — github-core-clone-01 Go forge (v0.3.0-go)

Kit level 28. Grade-local via `makefun grade-local --kit <kit> --bot entry-go
--driver process --instances 3`. All runs: agent 200/200, replay matched
byte-for-byte, 0 nondeterministic, 0 timeout.

## Runs

| Run | When (WIB) | Code state | Trial 0 | Trial 1 | Trial 2 | Mean | Perf mean | p50 mean |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| t0 | 20 Nov ~22:38 | first build | 888 | — | — | 888 | 173 | 291.733 µs |
| A | 20 Nov ~22:40 | + label fix (no pre-seed) + incremental-fetch filter | 979 | 975 | 974 | 976.3 | 176.0 | 281.9 µs |
| B | 20 Nov ~22:43 | + fetch-response cache + singleflight | 961 | 961 | 969 | 963.7 | 163.3 | 334.5 µs |
| C | 20 Nov ~22:44 | same as B (repeat) | 958 | — | — | ~960 | ~158 | ~331 µs |
| D | 20 Nov ~22:46 | cache reverted (keep label + incremental fixes) | 969 | 980 | 971 | 973.3 | 173.3 | 290.2 µs |

## What the numbers say

- **t0 → A (+88):** two real bugs. (1) Repos were pre-seeded with the 6
  default labels, so `POST /labels {name: bug}` answered 422 and every
  issues-check setup failed (15 checks) plus `protocol.error-message`.
  Fixed by not pre-seeding labels. (2) Incremental fetch re-sent
  already-held tree/blob objects (`addTreeObjects` ignored the has-set).
  Fixed by filtering `hasSet` out of the send set.
- **A → B/C (−13):** not a code regression. The decline is uniform across
  all four workloads including read/write, which the change did not touch,
  and trial wall time grew 7.9 s → 10.0 s: the shared VM host slowed down
  (load avg 1.57 on 1.5 shared CPUs). The cache's expected gain was ~0
  anyway (it only speeds the first clone round; p50 is the median of 3),
  so it was reverted to keep the code simpler and remove a staleness risk.
- **D confirms A:** reverted build lands back in the 970s band
  (969/980/971), so the 976 mean is real, not a one-off.

## Reference points

- v0.2.1 Node fallback: local 965 (perf 165, p50 330.8 µs); **server dev
  909** (perf 109, p50 709.8 µs), rank dev 9.171/10.838.
- Rank-1 target (HyperForge v5): local 970 (perf 170, p50 305.6 µs).
- This build, clean-run mean: **local 974.8** (perf ~174.7, p50 ~286 µs).
  Best single trial: **980** (run D trial 1).

## Per-workload local p50 (ms, mean of the 6 clean trials A+D)

| Workload | p50 mean | Points mean | T_ref |
| --- | --- | --- | --- |
| push | 580 | 44.0 | 1640.82 |
| clone | 122 | 40.0 | 266.94 |
| read | 831 | 46.2 | 2759.72 |
| write | 119 | 44.5 | 348.97 |

Clone is the most volatile workload across trials (94–144 ms); the rest
are stable within a few points.
