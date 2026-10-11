# Deploying gnofacilitator

An x402 facilitator for one gno chain. It exposes `/verify`, `/settle` and `/supported`, and
**holds no keys**: it verifies payments offline and relays transactions the payer already
signed. A compromised facilitator cannot move funds anywhere the payer did not sign for.

Stateless. No database, no volumes, no migrations, no secrets. The only state is in-memory
rate-limit buckets, which are swept.

## Artifact

```
ghcr.io/gnoverse/gnofacilitator:<version>
```

**0.1.0 cannot serve onyx-1.** It links a gno older than the one onyx-1 runs and cannot decode
the node's simulation reply. Payments that reach simulation answer `503`, valid ones included.
Deploy a later release.

| | |
|---|---|
| Platforms | `linux/amd64`, `linux/arm64` |
| Base | `gcr.io/distroless/static-debian12:nonroot` (no shell — `docker exec` cannot give you one) |
| User | `65532`, non-root |
| Entrypoint | `/usr/local/bin/gnofacilitator` |
| Registry auth | none — the package is public |

Pin by digest, not by tag: `latest` moves, and only a digest is what provenance attests.
`docker buildx imagetools inspect ghcr.io/gnoverse/gnofacilitator:<version>` prints the image's
digest.

Verify before deploying:

```sh
gh attestation verify oci://ghcr.io/gnoverse/gnofacilitator:<version> --repo gnoverse/x402
docker run --rm ghcr.io/gnoverse/gnofacilitator:<version> --version    # -> <version>
```

## Configuration

Flags only — the binary reads no environment variables.

| Flag | Required | Default | Meaning |
|---|---|---|---|
| `-rpc` | **yes** | — | gno chain RPC URL |
| `-chain-id` | **yes** | — | chain id; the service advertises the network as `gno:<chain-id>` |
| `-listen` | no | `:8402` | listen address, all interfaces |
| `-rate-per-second` | no | `10` | sustained per-peer rate on `/verify` and `/settle` |
| `-rate-burst` | no | `20` | largest tolerated per-peer spike |
| `-version` | no | — | print version and exit |

`-rpc` and `-chain-id` **must describe the same chain.** They are configured separately and a
disagreement is invisible in a payment: every signature verifies against the wrong sign doc
and is refused as invalid, blaming payers for an operator error.

```sh
docker run -d --name gnofacilitator \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 8402:8402 \
  ghcr.io/gnoverse/gnofacilitator@sha256:<digest> \
  -rpc https://rpc.onyx.testnets.gno.land:443 \
  -chain-id onyx-1
```

Verified: it starts and serves under `--read-only`, `--cap-drop ALL` and
`no-new-privileges`. It writes nothing to disk and needs no capabilities — it binds a port
above 1024.

## Endpoints

| Method | Path | Caller | Notes |
|---|---|---|---|
| POST | `/verify` | resource server | rate limited; queries the node |
| POST | `/settle` | resource server | rate limited; broadcasts to the node |
| GET | `/supported` | anyone | static; no node access |

Callers are **resource servers, server-to-server** — not browsers. No CORS headers are set.

`/supported` response shape, useful as a smoke test:

```json
{"kinds":[{"x402Version":2,"scheme":"exact","network":"gno:onyx-1"}],"extensions":[],"signers":{}}
```

## Health probes

**There is no health endpoint.** Use `GET /supported` for liveness.

**It is not a readiness signal.** Verified: with an unreachable `-rpc`, the container starts,
logs a warning, and `/supported` still answers `200` — while every `/verify` and `/settle`
would answer `503`. If you need readiness to reflect the chain, probe the node's RPC
separately; this service cannot tell you.

## Rate limiting behind a proxy

The throttle keys on the connection's real address. **`X-Forwarded-For` and `X-Real-IP` are
deliberately ignored** — the service authenticates nobody, so a caller could set either
freely and buy unlimited quota by rotating the value.

Consequence: **behind a reverse proxy every client collapses into a single bucket.** Throttle
upstream, at the proxy, and treat `-rate-per-second` / `-rate-burst` as a backstop rather than
the control.

IPv4 keys the host address; IPv6 keys the `/64`, because a single host sources requests from
any address in a routed `/64` at no cost.

Over-limit callers get `429`.

## Timeouts

| | |
|---|---|
| read header | 5 s |
| read | 10 s |
| write | 30 s |
| idle | 60 s |

`/settle` waits on a chain broadcast, so proxy and load-balancer timeouts must exceed the
30 s write timeout — otherwise the proxy cuts a settle the chain may still commit.

## Startup

The process queries the node for its chain id, retrying for up to 10 s:

| Outcome | Behaviour |
|---|---|
| node reports a different chain than `-chain-id` | **fatal**, exit 1 |
| node does not answer within 10 s | warn, start anyway |
| match | start |

So a chain/RPC mismatch is caught at boot whenever the node is reachable. A missing `-rpc` or
`-chain-id`, a `-chain-id` containing `:`, or a negative rate flag exits 2 before listening.
An `-rpc` the RPC client rejects exits 1. A malformed one it accepts fails the chain-id query like
an unreachable node, so the process warns and starts.

## Shutdown — no graceful handler

Verified: `SIGTERM` terminates the process in ~0.2 s, observed exit code **2**. There is no
signal handler and no `http.Server.Shutdown`, so in-flight requests are cut.

Two consequences:

- **A `/settle` cut mid-flight may already have broadcast.** The payment can land on chain
  while the resource server sees a failed settle. Drain at the proxy before stopping.
- A normal stop produces a **non-zero exit code**, which some orchestrators and dashboards
  read as a crash. Expect it; don't chase it.

## Failure modes

| Condition | Response | Meaning |
|---|---|---|
| node unreachable or silent | `503`, no reason code | deliberately *not* a payment verdict — the facilitator will not tell a seller a payment failed when it could not check |
| payment fails a check, or the chain refused the transaction | `200` with `isValid:false` (`/verify`) or `success:false` (`/settle`) and a reason code | an established outcome |
| over rate limit | `429` | |
| body is not one JSON request, or exceeds 1 MiB | `400`, plain text, no reason code | |

The distinction in row 1 matters operationally: a `503` means "ask again", not "the buyer
didn't pay".

## Security posture

- **No keys, no secrets, no credentials.** Nothing to provision, rotate, or leak. There is no
  wallet and no funded account.
- **It relays anything well-formed.** By design — it is a public relay. Transaction fees are
  paid by the payer inside the signed transaction, never by the operator, so the abuse surface
  is **RPC load, not funds**. Size the node and the upstream throttle accordingly.
- Runs non-root, read-only rootfs, zero capabilities (all verified above).
- Distroless: no shell, no package manager. Debug from logs and from outside the container.
- No TLS — terminate upstream. The payment payload is a bearer credential on the wire, so
  the public endpoint must be HTTPS.
- Logs are `log/slog` text to stderr and contain payer addresses and transaction hashes,
  which are public chain data; they carry no secrets.

## Decisions the deployer makes

1. **Which chain**, and an RPC endpoint serving exactly that chain.
2. **A public HTTPS endpoint** — resource servers call it from their own backends.
3. **Upstream throttling**, since the built-in limiter cannot see individual clients behind a
   proxy.
4. **Pin a digest**, and keep a record of which digest is running.
