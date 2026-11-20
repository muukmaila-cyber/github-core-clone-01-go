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
| E | 20 Nov ~23:12 | iterasi 1: Sscanf→hex manual, pktLine manual, isAncestor DFS, parse byte-based, pack singleflight, issue-JSON cache, auth precompute, zlib writer pool | 983 | 981 | 980 | 981.3 | 181.3 | 259.9 µs |
| F | 20 Nov ~23:18 | iterasi 2: GOMAXPROCS=2 + GC off | 977 | 976 | 975 | 976.0 | 176.0 | 282.5 µs |
| G | 20 Nov ~23:20 | iterasi 2 REVERTED (= E) | 980 | 978 | 985 | 981.0 | 181.0 | 261.7 µs |
| H | 20 Nov ~23:27 | iterasi 3a: `-gcflags=all=-B` (matikan bounds check) | 981 | 974 | 980 | 978.3 | 178.3 | 273.3 µs |
| I | 20 Nov ~23:29 | iterasi 3 REVERTED (= E/G, binary commit) | 966 | 979 | 982 | 975.7 | 175.7 | 284.8 µs |
| J | 20 Nov ~23:34 | iterasi 4: pooled zlib readers (sync.Pool + Reset di parsePack) | 971 | 974 | 981 | 975.3 | 175.3 | 284.4 µs |

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
- **D → E (+6.5, 974.8 → 981.3):** hot-path optimization batch 1. Biggest
  win: `isAncestor` used to build the FULL ancestor set per push
  (O(chain²) per 200-push round); now a DFS with early exit (usually 1
  step on a linear chain) — push 580 → 501 ms. `fmt.Sscanf` per pkt-line
  and `fmt.Sprintf` per pkt-line replaced by manual hex; commit/tree
  parsing switched from `strings.Split(string(data))` to byte scanning;
  pack builds deduplicated across the 50 concurrent clones
  (singleflight); GET-issue responses cached (the read workload GETs the
  same issue 2,000x); auth header precomputed; pooled zlib writers.
  Clone 122 → 99 ms, read 831 → 764 ms, write flat 119 → 120 ms.
- **E → F (−5.3, 981.3 → 976.0):** GOMAXPROCS=2 + GC off REGRESSED.
  Push 501 → 533 ms, clone 99 → 127 ms (read flat). Fewer P's serialize
  the 50 concurrent clones; reverted.
- **F → G (+5.0, 976.0 → 981.0):** revert confirmed — back to the E
  band (980/978/985, best single trial 985). E+G combined mean: **981.2**
  (perf 181.2, p50 ~261 µs). Trial-to-trial noise is ±3 points
  (978–985); clone is the most volatile workload (rounds 89–172 ms).
- **Iterasi 3a (flag compiler `-gcflags=all=-B`):** run H 978.3
  (981/974/980), perf 178.3 — NO gain vs E/G 981.2, dalam noise.
  Push sempat 50 poin (418,5 ms, trial 0) tapi clone/read turun;
  host juga melambat saat run H (grade 94 dtk vs ~60 dtk biasa).
  Di-revert; run I (binary commit, plain flags) 975.7 (966/979/982)
  dengan trial 0 clone 230,9 ms (outlier, load VM 1,81 di awal run).
  Trials 1–2 run I: 979/982 — kembali di band E/G.
  **Plafon terkonfirmasi: 12 trial (E, G, H, I) semua di 966–985,
  mean ~978–981.** Yang sengaja TIDAK dicoba: `GOAMD64=v3` (binary
  butuh AVX2 — CPU server tidak diketahui, risiko illegal-instruction
  crash = skor 0, untuk gain yang tak terukur) dan pooled zlib readers
  (~1 ms per trial, 0,2% — di bawah noise ±3 poin).
- **Iterasi 4 (pooled zlib readers, atas pilihan user):** run J 975.3
  (971/974/981), perf 175.3. Push 506 → 479 ms (+1 poin, satu-satunya
  workload yang disentuh parsePack) — tapi push di run H/I (binary
  commit, tanpa pool) juga 484/446 ms: perbaikan push adalah varians
  host, bukan efek pool. Clone/read/write (tidak disentuh) justru
  turun karena host melambat — total run di bawah baseline E/G 981,2.
  Kesimpulan: gain tak terbukti → **DI-REVERT**, kode kembali ke
  v0.3.1-go commit f096d8e (binary sama). 15 trial total (E–J):
  plafon **~975–981**.

## Why 999 is blocked (measured, 20 Nov ~23:20 WIB)

- Target 999 needs perf 199/200, i.e. per-workload: push ≤434 ms,
  clone ≤71 ms, read ≤730 ms, write ≤92 ms. Now (E+G mean): push 506,
  clone 103, read 758, write 118 ms — gaps −13%, −29%, −4%, −23%.
- Micro-benchmark vs the standalone forge (Python client, unloaded VM):
  read 0.135 ms/req vs a bare-root floor of 0.130 ms/req — server read
  work is ~5 µs/req, already at the loopback floor. Write 0.173 ms/req
  vs 0.130 floor — server write work ~43 µs/req. The kit's Node client
  measures 0.38 ms/read and 0.60 ms/write: the remaining ~0.25–0.43
  ms/req is the client's own undici + JSON + await cost on the shared
  1.5-CPU VM, which no server change can move. Same for clone (client
  does 3 HTTP exchanges + checksum per clone) and push (client-side git
  exchange). Server CPU is now a small share of every workload's wall.
- Server ceiling: host is 2.146× slower than this VM (v0.2.1: local p50
  330.8 µs → server 709.8 µs). Local 981 → server estimate **926**
  (perf 126). Even a hypothetical local 999 → server ~944. Matching
  HyperForge v5's server 970 (p50 305.6 µs) needs local p50 ~143 µs vs
  the current ~261 µs (−45%) — below what the kit client + loopback
  floor allow for read/clone. Further server-side iterations are
  expected to move ±0–3 points, inside the noise band.

## Reference points

- v0.2.1 Node fallback: local 965 (perf 165, p50 330.8 µs); **server dev
  909** (perf 109, p50 709.8 µs), rank dev 9.171/10.838.
- Rank-1 target (HyperForge v5): **server** 970 (perf 170, p50 305.6 µs).
  (Earlier note calling it a local number was wrong: it is the
  leaderboard/server dev score; matching it on server needs local p50
  ~143 µs.)
- This build, clean-run mean (E+G, 6 trials): **local 981.2**
  (perf ~181.2, p50 ~261 µs). Server estimate **~926**.
  Best single trial: **985** (run G trial 2).

## Per-workload local p50 (ms, mean of the 6 clean trials E+G)

| Workload | p50 mean | Points mean | T_ref | Needed for 999 |
| --- | --- | --- | --- | --- |
| push | 506 | 46.3 | 1640.82 | ≤434 (−13%) |
| clone | 103 | 42.0 | 266.94 | ≤71 (−29%) |
| read | 758 | 48.2 | 2759.72 | ≤730 (−4%) |
| write | 118 | 44.7 | 348.97 | ≤92 (−23%) |

Clone is the most volatile workload across trials (94–144 ms); the rest
are stable within a few points.
