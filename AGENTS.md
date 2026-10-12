# rreading-glasses (BryanLabs fork)

## What it is

A Go metadata server that acts as a drop-in replacement for the defunct Readarr/Lidarr
official metadata service. Upstream repo: `blampe/rreading-glasses`. This fork adds
BryanLabs-specific fixes and tuning not yet merged upstream.

The `hardcover` tag uses the Hardcover GraphQL API as the metadata source. The `latest`
tag uses GoodReads (unrelated to the BryanLabs deployment).

## Where it's used

- Namespace: `media-suite`
- Deployment: `rreading-glasses-hardcover`
- Image: `ghcr.io/bryanlabs/rreading-glasses:hardcover`
- Companion: Postgres (required for persistent cache; any version works)
- Readarr/Lidarr point their "Metadata Provider Source" at this service's cluster address

A separate upstream `blampe/rreading-glasses` deployment also runs in the `default`
namespace. The two are independent; do not conflate them.

## BryanLabs customizations

Commits on top of upstream. The deployed image is built from
`fix/hardcover-monitor-best-edition`, which stacks on `fix/hardcover-search-ids-int64`;
neither is merged into `main`. The image is pinned by digest in bare-metal
`cluster/media-suite/rreading-glasses-hardcover/deployment.yaml`.

1. **Throttle raised to 30 req/s** (`89e836f`): `internal/controller.go`
   `NewUpstream()` raised the ticker from 3 req/s to 30 req/s. Background worker
   concurrency limits also lifted: `refreshG` raised to 100, `workG` raised to 50.
   Comment in code: "many cold-path book lookups chain 3-5 sequential queries; the old
   333ms throttle alone was adding 1-2s per cold load."

2. **Search IDs decoded as int64** (`f31b915`): `hardcover/generated.go`,
   `hardcover/schema.graphql`, `internal/hardcover.go`. Hardcover changed their GraphQL
   search response to serialize IDs as numbers rather than strings, breaking all searches
   with HTTP 500. This mirrors upstream PR blampe/rreading-glasses#549 (not yet merged
   upstream at time of commit).

3. **ALLOW_PENDING_BOOKS env var** (`29ca600`): `cmd/rghc/main.go`,
   `internal/hardcover.go`. Adds a safety-net env var (defaults true) so that if
   upstream re-adds pending book filtering, BryanLabs deployments are not disrupted.

4. **Best edition wins Readarr's pick** (`e84a098`): `internal/hardcover.go`. Readarr
   monitors the edition with the highest votes times rating, and every edition carries
   the work's ratings, so they tied and the lowest edition ID won, often a translation
   (The Hero with a Thousand Faces was added as "El héroe de las mil caras"). The
   `BestBookId` edition now gets one extra vote. Books already in Bookshelf keep their
   monitored edition; change it with `PUT /api/v1/book/{id}` and an `editions` list.

Local builds need `CGO_ENABLED=0` (the Mac's linker rejects the macOS 27 SDK), and
`env -u HARDCOVER_API_KEY` so the live-API tests skip. `TestMergedWorks`,
`TestQueryBuilderMultipleQueries` and the Postgres tests fail on this branch already.

**WIP stash**: `stash@{0}` on branch `feat/allow-pending-books-isbn-search` contains
uncommitted work titled "WIP: remove isbnSearchKey context (pre-INF-search-fix)". Do not
drop this stash without understanding the intent.

## How it works

Request flow (Hardcover mode):

1. Readarr/Lidarr sends HTTP requests to the service (port 8788) for authors, works,
   editions, search, and recommendations.
2. `internal/handler.go` routes requests to `Controller` methods.
3. `Controller` checks a layered cache (in-memory Ristretto + Postgres as backing store).
   Cache keys are prefixed: `a` = author, `w` = work, `b` = book/edition, `s` = series,
   `z` = ASIN, `i` = ISBN.
4. On cache miss, `HCGetter` (`internal/hardcover.go`) issues GraphQL queries to
   `hardcover.app` via `hardcover/queries.graphql` (code-generated structs in
   `hardcover/generated.go`).
5. Large author backlogs are loaded asynchronously through bounded worker pools
   (`refreshG`/`workG`). Denormalization (embedding editions into works, works into
   authors) runs through a channel (`denormC`) to serialize updates.
6. In-flight author refreshes survive restarts via the `Persister` (Postgres table).
7. Cache TTLs: authors 7 days, works 14 days, editions 28 days, series 14 days, 404s 7 days.
   All are fuzzed to spread expiry.

## Code map

| Path | Purpose |
|------|---------|
| `cmd/rghc/main.go` | Entry point for Hardcover binary |
| `internal/controller.go` | Core orchestration: cache, singleflight, workers, TTLs, throttle |
| `internal/handler.go` | HTTP routing (chi) |
| `internal/hardcover.go` | `HCGetter`: maps Hardcover GraphQL responses to internal types |
| `hardcover/queries.graphql` | GraphQL queries against Hardcover API |
| `hardcover/generated.go` | Code-generated structs (run `go generate ./hardcover/`) |
| `hardcover/glue.go` | Type-switch helpers for generated contribution types |
| `internal/cache.go` | Layered cache interface + `LayeredCache` (memory + Postgres) |
| `internal/persist.go` | Postgres-backed author-refresh state |
| `internal/postgres.go` | Postgres cache implementation |
| `internal/schema.sql` | DB schema |
| `internal/resources.go` | Wire types (`AuthorResource`, `workResource`, `bookResource`) |
| `internal/transport.go` | `throttledTransport` (rate-limiting HTTP transport) |

## Build & deploy

```bash
# Build AMD64 image on worker1, then pin the new digest in bare-metal
docker buildx build \
  --builder worker1 \
  --platform linux/amd64 \
  --build-arg RGPATH=./cmd/rghc \
  -t ghcr.io/bryanlabs/rreading-glasses:hardcover-<sha> \
  --push .
```

The Dockerfile is a two-stage build: `golang:1.26.0-alpine` compiles the binary,
`gcr.io/distroless/static:nonroot` is the runtime image. Set `RGPATH=./cmd/rghc` to
select the Hardcover binary (omitting it builds the GoodReads binary by default).

Hardcover API key expires every January 1. Rotate `--hardcover-auth="Bearer <token>"`
in the deployment secret before then.

## Gotchas

- **WIP stash**: `stash@{0}` on `feat/allow-pending-books-isbn-search` is unfinished
  work on ISBN search context. Check intent before dropping.
- **Throttle is now 30 req/s**: upstream default was 3 req/s. If Hardcover starts
  rate-limiting, dial back the ticker in `NewUpstream()` in `internal/controller.go`.
- **Search IDs fix is not yet upstream**: if you rebase onto upstream, `f31b915` must be
  reapplied or searches will 500.
- **Memory is auto-limited to 85% of available**: set a container memory limit in the
  Deployment so Ristretto sizes itself correctly.
- **Hardcover backend only**: this fork exclusively deploys the `hardcover` tag.
  The GoodReads (`gr/`) code path is untested in BryanLabs infra.
- **StorageClassName**: use `topolvm-ssd-xfs` for the Postgres PVC.
