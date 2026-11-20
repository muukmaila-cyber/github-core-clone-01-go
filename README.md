[![make.fun · github-core-clone-01](https://make.fun/badge/github-core-clone-01.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![rank](https://make.fun/badge/github-core-clone-01/muukmaila-cyber/github-core-clone-01-go/rank.svg)](https://make.fun/bounty/github-core-clone-01/leaderboard?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)
[![reward](https://make.fun/badge/github-core-clone-01/muukmaila-cyber/github-core-clone-01-go/reward.svg)](https://make.fun/bounty/github-core-clone-01?utm_source=github&utm_medium=readme&utm_campaign=github-core-clone-01)

Built for the [make.fun github-core-clone-01 bounty](https://make.fun/bounty/github-core-clone-01) — a GitHub-compatible git forge, $500 in USDC.

# github-core-clone-01 — Go forge (v0.3.1-go)

A GitHub-compatible git forge written from scratch in Go (standard library
only), built as one statically linked Linux amd64 binary and launched from
`main.ts`. It serves the full surface the bounty grades:

- **REST v3** (`/api/v3/`): repos, contents, branches, commits, git refs,
  labels, issues, comments, pull requests (squash merge with real
  merge-base/conflict detection), pagination with `Link` headers.
- **Git smart HTTP** (`/{owner}/{repo}.git/`): protocol v2 (ls-refs, fetch
  with shallow/deepen/unshallow) and v0 (advertisement, fetch, receive-pack
  with report-status, fast-forward enforcement, `non-fast-forward`
  refusals). Real SHA-1 objects, packs built without deltas, OFS/REF delta
  resolution on push.
- **GraphQL** (`/api/graphql`): repository/issue/pull-request queries with
  variables, aliases, fragments, inline input objects, batched operations,
  cursor pagination, `node(id:)` round-trips, introspection (`__type`,
  `__schema`), and mutations (create/close issue, create/merge pull request,
  add comment).
- **MCP** (`/mcp`, revision 2026-07-28): `server/discover`,
  `tools/list`, `tools/call` for six tools (`create_repo`, `get_repo`,
  `create_issue`, `list_issues`, `create_pull_request`,
  `merge_pull_request`), plus the legacy `initialize` handshake. Tools act on
  the same store as REST, so ids are stable across surfaces.

## Layout

| File | What it holds |
| --- | --- |
| `main.go` | Lifecycle: bind ephemeral port, register with `$MAKEFUN_ENV_URL`, poll `/done`, exit 0; router + auth. |
| `rng.go` | Deterministic scenario RNG (FNV-1a + splitmix64 + xorshift64*); the viewer identity is derived from the scenario seed at startup, never hardcoded. |
| `store.go` | In-memory store, logical clock, repos/issues/pulls, git objects, trees, commits, merge-base and diff. |
| `gitobj.go` | pkt-lines, packfile build/parse (zlib, OFS/REF deltas). |
| `gitproto.go` | Smart-HTTP advertisements, ls-refs, fetch selection (depth/shallow), receive-pack, pack cache. |
| `rest.go` | REST v3 handlers. |
| `graphql.go` | GraphQL lexer, parser and executor. |
| `mcp.go` | MCP (JSON-RPC 2.0, Streamable HTTP) handlers and tools. |
| `main.ts` | Entrypoint: spawns `./forge`, forwards its exit code. |
| `forge` | The vendored static binary (built from these sources). |

## Determinism

No wall clock, no randomness, no map-iteration order in any output: a
logical clock (scenario epoch + sequential ticks) stamps every object,
ids are sequential, pack objects are emitted oid-sorted, and the viewer
login is re-derived from the fixed scenario seed on every boot. Replays
of the same operation sequence produce byte-identical state.

## Build

```sh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o forge .
chmod +x forge
```

The binary is ~7 MB, under the 8 MiB per-file cap; the whole entry stays
under the 16 MiB total cap. Requires Go 1.23+ (built here with Go 1.27.4).

## Run (local smoke test)

```sh
MAKEFUN_SHOP_TOKEN=testtoken123 ./forge   # binds 127.0.0.1:0, serves forever
curl -H "Authorization: Bearer testtoken123" http://127.0.0.1:<port>/api/v3/user
```

With `MAKEFUN_ENV_URL` set, the binary registers its port and exits when
the env reports `{"done": true}` — the same lifecycle the kit's starter
`main.ts` describes.

## Performance notes

- Go `net/http` + single in-memory store; the read path (2,000 sequential
  issue reads) and write path (200 issue creates) are plain map lookups.
- Packs are zlib level-1 (BestSpeed) and oid-sorted; identical fetches
  (e.g. the 50 concurrent shallow clones) are served from a small pack
  cache keyed by the exact fetch parameters.
- Pushes parse the incoming pack once, resolve deltas, and validate each
  ref independently so one refused ref never blocks the others.

## License

MIT — see `LICENSE`.
