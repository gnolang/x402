# Deploying gnofacilitator

An x402 facilitator for one gno chain. It exposes `/verify`, `/settle` and `/supported`, and
**holds no keys**: it verifies payments offline and relays transactions the payer already
signed. A compromised facilitator cannot move funds anywhere the payer did not sign for.

No database, no volumes, no migrations, no secrets. The only state is in memory: rate-limit
buckets, which are swept, and the transaction hashes of the payments it settled or left pending in
the last five minutes. They make each payment settle once, and let a pending one be settled again
without a second broadcast. A restart empties them.

**Run one facilitator process per chain.** That record is per process. Two replicas behind a
balancer can each settle the same payment through different nodes, and both report it settled
while the payee is paid once. One process deduplicates even through a load-balanced RPC.

## Artifact

The image is published at:

```text
ghcr.io/gnolang/gnofacilitator:<version>
```

Releases up to 0.3.0 are published as `ghcr.io/gnoverse/gnofacilitator`.

**0.1.0 cannot serve onyx-1.** It links a gno older than the one onyx-1 runs and cannot decode
the node's simulation reply. Payments that reach simulation answer `503`, valid ones included.
Deploy a later release. Each release links one gno version and verifies payments by that
version's rules, so it serves the chains running that version.

The image has these properties:

| Property | Value |
|---|---|
| Platforms | `linux/amd64`, `linux/arm64` |
| Base | `gcr.io/distroless/static-debian12:nonroot`, with no shell: `docker exec` cannot give you one |
| User | `65532`, non-root |
| Entrypoint | `/usr/local/bin/gnofacilitator` |
| Registry auth | none, the package is public |

Pin by digest, not by tag: `latest` moves, and only a digest is what provenance attests.
`docker buildx imagetools inspect ghcr.io/gnolang/gnofacilitator:<version>` prints the image's
digest.

Verify the image before deploying it:

```sh
gh attestation verify oci://ghcr.io/gnolang/gnofacilitator:<version> --repo gnolang/x402
docker run --rm ghcr.io/gnolang/gnofacilitator:<version> --version    # -> <version>
```

The release workflow of `gnoverse/x402` signed each gnoverse image's provenance, which the
`gnoverse` organization keeps, so verifying one names both:

```sh
gh attestation verify oci://ghcr.io/gnoverse/gnofacilitator:<version> --owner gnoverse --signer-repo gnoverse/x402
```

## Configuration

The binary reads flags only, no environment variables:

| Flag | Required | Default | Meaning |
|---|---|---|---|
| `-rpc` | **yes** | none | gno chain RPC URL |
| `-chain-id` | **yes** | none | chain id, 1 to 32 of `[-_a-zA-Z0-9]` (CAIP-2); the service advertises the network as `gno:<chain-id>` |
| `-listen` | no | `:8402` | listen address, all interfaces |
| `-rate-per-second` | no | `10` | sustained per-peer rate on `/verify` and `/settle` |
| `-rate-burst` | no | `20` | largest tolerated per-peer spike |
| `-version` | no | none | print version and exit |

`-rpc` and `-chain-id` **must describe the same chain.** They are configured separately and a
disagreement is invisible in a payment: every signature verifies against the wrong sign doc
and is refused as invalid, blaming payers for an operator error.

This runs it against onyx-1 with a read-only root filesystem and no capabilities:

```sh
docker run -d --name gnofacilitator \
  --read-only --cap-drop ALL --security-opt no-new-privileges \
  -p 8402:8402 \
  ghcr.io/gnolang/gnofacilitator@sha256:<digest> \
  -rpc https://rpc.onyx.testnets.gno.land:443 \
  -chain-id onyx-1
```

It serves under `--read-only`, `--cap-drop ALL` and `no-new-privileges`: it writes nothing to disk
and needs no capabilities, since it binds a port above 1024.

## Endpoints

The service has three endpoints:

| Method | Path | Caller | Notes |
|---|---|---|---|
| POST | `/verify` | resource server | rate limited; queries the node |
| POST | `/settle` | resource server | rate limited; broadcasts to the node |
| GET | `/supported` | anyone | static; no node access |

Callers are **resource servers, server-to-server**, not browsers. No CORS headers are set.

`/supported` answers in this shape, which makes it a smoke test:

```json
{"kinds":[{"x402Version":2,"scheme":"exact","network":"gno:onyx-1"}],"extensions":[],"signers":{"gno:*":[]}}
```

## Health probes

**There is no health endpoint.** Use `GET /supported` for liveness.

**It is not a readiness signal.** With an unreachable `-rpc`, the container starts, logs a
warning, and `/supported` still answers `200`, while every payment that passes the offline checks
answers `503` on `/verify` and `/settle`. If you need readiness to reflect the chain, probe the
node's RPC separately; this service cannot tell you.

## Rate limiting behind a proxy

The throttle keys on the connection's real address. **`X-Forwarded-For` and `X-Real-IP` are
ignored on purpose:** the service authenticates nobody, so a caller could set either freely and
buy unlimited quota by rotating the value.

**Behind a reverse proxy, every client shares one bucket.** Throttle upstream, at the proxy, and
treat `-rate-per-second` and `-rate-burst` as a backstop rather than the control.

IPv4 keys the host address; IPv6 keys the `/64`, because a single host sources requests from
any address in a routed `/64` at no cost. Over-limit callers get `429`.

## Timeouts

The service applies these timeouts:

| Timeout | Value |
|---|---|
| read header | 5 s |
| read | 10 s |
| write | 30 s |
| idle | 60 s |
| answer, a request waiting on the node | 25 s, then `503` |
| `/settle`, waiting for a block to execute a payment the node admitted | 10 s, inside the answer timeout, then `settlement_pending` |

`/settle` waits on a chain broadcast, so proxy and load-balancer timeouts must exceed the
30 s write timeout. Otherwise the proxy cuts a settle the chain may still commit.

## Startup

The process queries the node for its chain id, retrying for up to 10 s:

| Outcome | Behaviour |
|---|---|
| node reports a different chain than `-chain-id` | **fatal**, exit 1 |
| node does not answer within 10 s | warn, start anyway |
| match | start |

So a chain/RPC mismatch is caught at boot whenever the node is reachable. A missing `-rpc` or
`-chain-id`, a `-chain-id` outside CAIP-2's reference grammar, or a negative rate flag exits 2
before listening.
An `-rpc` the RPC client rejects exits 1. A malformed one it accepts fails the chain-id query like
an unreachable node, so the process warns and starts.

## Shutdown

`SIGTERM` or `SIGINT` stops new connections and lets requests in flight finish, for up to
30 s. A clean stop exits 0; a drain that runs out exits 1.

A request waiting on the node is answered within the 25 s answer timeout, so the drain
covers a `/settle` in flight. Give the orchestrator a stop grace period above 30 s so it
does not kill the process mid-drain.

## Failure modes

The facilitator answers each condition this way:

| Condition | Response | Meaning |
|---|---|---|
| node unreachable | `503`, no reason code | not a payment verdict: the facilitator will not tell a seller a payment failed when it could not check |
| node accepts the connection and never answers | `503` after 25 s, no reason code | the answer timeout; no verdict, as for an unreachable node |
| node admitted the payment and no block executed it within 10 s | `200`, `success:false`, `settlement_pending`, with the transaction hash | not a failure: the payment may still commit, and the seller SDKs settle once more without a second broadcast |
| payment fails a check, or the chain refused the transaction | `200` with `isValid:false` (`/verify`) or `success:false` (`/settle`) and a reason code | an established outcome |
| `/settle` of a payment this process is settling, or settled in the last five minutes | `200`, `success:false`, `duplicate_settlement`, with the transaction hash once a node admitted the payment | the first settle answers for it |
| over rate limit | `429` | |
| body is not one JSON request, or exceeds 1 MiB | `400`, plain text, no reason code | |

A `503` means "could not check", not "the buyer didn't pay", and the buyer can present the payment
again. After a `/settle` answered `503`, check the chain first.

A `/settle` answered `503` starts no broadcast after that answer. A broadcast the node never
answered may have reached its mempool and may still commit. Settling the same payment again then
answers `invalid_exact_gno_payload_sequence_mismatch`: the payer's sequence is spent, possibly
by that payment. Look the transaction up on chain before refusing the buyer. The hash to look up
is the hex SHA-256 of the bytes the payload's `transaction` decodes to.

## Security posture

The facilitator's exposure comes down to these points:

- **No keys, no secrets, no credentials.** Nothing to provision, rotate, or leak. There is no
  wallet and no funded account.
- **It relays anything well-formed.** It is a public relay by design. Transaction fees are paid by
  the payer inside the signed transaction, never by the operator, so the abuse surface is **RPC
  load, not funds**. Size the node and the upstream throttle accordingly.
- It runs non-root, with a read-only root filesystem and no capabilities.
- Distroless: no shell, no package manager. Debug from logs and from outside the container.
- No TLS: terminate it upstream. The payment payload is a bearer credential on the wire, so
  the public endpoint must be HTTPS.
- Logs are `log/slog` text to stderr and contain payer addresses and transaction hashes,
  which are public chain data; they carry no secrets.

## Decisions the deployer makes

A deployment fixes these four points:

- **Which chain**, and an RPC endpoint serving exactly that chain.
- **A public HTTPS endpoint**, since resource servers call it from their own backends.
- **Upstream throttling**, since the built-in limiter cannot see individual clients behind a
  proxy.
- **A pinned digest**, with a record of which digest is running.
