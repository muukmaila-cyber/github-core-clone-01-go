# Final estimate — github-core-clone-01 Go forge (v0.3.0-go)

## Local (measured, grade-local, kit level 28, 6 clean trials)

- Feature: **600/600** (120/120 checks, every run)
- Agent (MCP): **200/200** (15/15, every run)
- Perf: **~175/200** (range 173–179 per run)
- Total: **~975/1000** (run means 976.3 and 973.3; best single trial 980)
- Replay: matched byte-for-byte in every trial; 0 nondeterministic.

## Server (estimated, NOT measured)

The server host is slower than this VM. The only server data point is
v0.2.1: local p50 330.8 µs → server p50 709.8 µs, a **2.146× slowdown**,
perf 165 → 109. Applying that same factor per workload to this build's
local p50s:

| Run | Server perf est. | Server total est. |
| --- | --- | --- |
| A (3 trials) | 120.3 | ~920 |
| D (3 trials) | 118.7 | ~919 |

**Server estimate: ~919–920** (feature 600 + agent 200 + perf ~120).

Caveats, stated plainly:

- The 2.146× factor is a single observation from one entry (v0.2.1). The
  true per-workload slowdown on the server may differ; clone is the most
  volatile workload locally and the least certain in the estimate.
- The rank-1 target of 970 was a LOCAL-conditions number (HyperForge v5,
  p50 305.6 µs on a fast host). On the server host that same entry would
  land near ~915–920 too — this build is at that level, not above it by a
  proven margin.
- No rank claim is made. Final advancement needs top 60; with an estimated
  ~920 this entry is meaningfully ahead of the submitted v0.2.1's server
  909 (+11), but whether that clears the top-60 cut is unknown until the
  server grades it.

## What would change the estimate

- A faster clone round on the server (the pack cache already makes
  repeat clones cheap; the first-clone build is the remaining cost).
- If the server host is faster than the 2.146× factor implies, perf
  recovers roughly +12 points per 10% less slowdown (log scale).
