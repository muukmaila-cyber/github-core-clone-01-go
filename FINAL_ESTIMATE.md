# Final estimate — github-core-clone-01 Go forge (v0.3.1-go, optimasi iterasi 1)

## Local (measured, grade-local, kit level 28)

- Feature: **600/600** (120/120 checks, every run)
- Agent (MCP): **200/200** (15/15, every run)
- Perf: **~181/200** (runs E+G: 181.3 and 181.0; trials 978–985)
- Total: **~981/1000** (run means 981.3 and 981.0; best single trial 985)
- Replay: matched byte-for-byte in every trial; 0 nondeterministic.

Per-workload local p50 (mean E+G, 6 trials): push 506 ms (46.3 pts),
clone 103 ms (42.0), read 758 ms (48.2), write 118 ms (44.7).

## Server (estimated, NOT measured)

The server host is slower than this VM. The only server data point is
v0.2.1: local p50 330.8 µs → server p50 709.8 µs, a **2.146× slowdown**,
perf 165 → 109. Applying that same factor per workload to this build's
local p50s:

| Run | Server perf est. | Server total est. |
| --- | --- | --- |
| E (3 trials) | 126 | ~926 |
| G (3 trials) | 126 | ~926 |

**Server estimate: ~926** (feature 600 + agent 200 + perf ~126).

Caveats, stated plainly:

- The 2.146× factor is a single observation from one entry (v0.2.1). The
  true per-workload slowdown on the server may differ; clone is the most
  volatile workload locally and the least certain in the estimate.
- CORRECTION (20 Nov ~23:20 WIB): HyperForge v5's 970 (perf 170, p50
  305.6 µs) is the leaderboard SERVER dev score, not a local number.
  Matching it on server needs local p50 ~143 µs vs this build's ~261 µs.
  This build's server estimate ~926 is +17 over the submitted v0.2.1's
  server 909, but still −44 behind rank 1's 970.
- No rank claim is made. Final advancement needs top 60; whether ~926
  clears the top-60 cut is unknown until the server grades it.

## Gap to the user's 999 target (local)

999 needs perf 199/200: push ≤434 ms, clone ≤71 ms, read ≤730 ms,
write ≤92 ms. Now: 506 / 103 / 758 / 118 ms. Gap: 18 points.
Micro-benchmarks (standalone forge, Python client) show read/write
server work already at the loopback floor (read 0.135 ms/req vs 0.130
floor; write 0.173 vs 0.130) — the rest is the kit Node client's own
cost on the shared 1.5-CPU VM, which no server change can move.
Iteration 2 (GOMAXPROCS=2 + GC off) regressed 981.3 → 976.0 and was
reverted. Further server-side iterations are expected to move ±0–3
points, inside the ±3 trial noise band.

## What would change the estimate

- A faster clone round on the server (the pack cache already makes
  repeat clones cheap; the first-clone build is the remaining cost).
- If the server host is faster than the 2.146× factor implies, perf
  recovers roughly +12 points per 10% less slowdown (log scale).
