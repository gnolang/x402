# Releasing

Three components, three independent version lines. Releasing one does not touch the others.

| Component | Tag | Produces |
|---|---|---|
| `facilitator` | `vX.Y.Z` | archives + GitHub release + `ghcr.io/gnoverse/gnofacilitator`, attested. Also the Go module version. |
| `client` | `client/vX.Y.Z` | `@gnoverse/x402-gno` on npm, with provenance when the workflow publishes it |
| `weather` | `weather/vX.Y.Z` | `ghcr.io/gnoverse/x402-weather`, the demo seller's image for amd64 and arm64, attested; `latest` follows plain releases only |

All three run from the manual `release.yml` workflow, except the client while npm/cli#9969 is open
(see [Credentials](#credentials)). Nothing releases on push or merge.

## Release the facilitator

```sh
gh workflow run release.yml --ref main -f component=facilitator -f version=0.2.0
```

The run has two jobs. `facilitator-test` holds a read-only token and runs `go test ./...` and the
e2e, after `npm ci` installs exactly the lockfile. Only when it passes does `facilitator` start:
it validates the version, builds the release as a snapshot, pushes the tag, publishes and attests.
The tests run code from every Go and npm dependency, install scripts included, so they never run
in the job that can push to the repository and to ghcr.io and mint OIDC tokens. That job restores
no Actions cache either, because the jobs that run dependency code write it.

## Release the client

Until [npm/cli#9969](https://github.com/npm/cli/issues/9969) is fixed the dispatch fails at
publish: release by hand as described under [Credentials](#credentials).

The published version comes from `package.json`. Bump it first, in its own PR:

```sh
npm version --no-git-tag-version 0.2.0
git commit -am "chore(release): bump the client to 0.2.0"
# merge to main, then:
gh workflow run release.yml --ref main -f component=client -f version=0.2.0
```

The workflow refuses the dispatch if `package.json` disagrees with `version`, or if that
version is already on the registry.

## Release the weather image

The version lives in the tag alone, so nothing needs a bump:

```sh
gh workflow run release.yml --ref main -f component=weather -f version=0.1.0
```

The run's summary names the image by digest, which is what a deployment pins. The image is built
from `docker/weather.Dockerfile`, which compiles the seller itself, so
`docker build -f docker/weather.Dockerfile .` from the repository root builds the same thing
locally.

## Rules

- **Release from `main` only.** The workflow refuses any other ref.
- **Never delete a `vX.Y.Z` tag.** It is a Go module version; once fetched, `sum.golang.org`
  binds it to that commit permanently, and re-cutting it breaks every downstream build. Cut
  the next patch number instead. `client/vX.Y.Z` and `weather/vX.Y.Z` have no such constraint.
- **Use `npm version`, not a hand edit.** It updates `package-lock.json` too, which `npm ci`
  does not check.
- **Renaming `release.yml` breaks npm publishing** until npm's trusted publisher is updated
  to the new filename.

## If a release fails

- **client** — publish runs before the tag, so a failed publish leaves nothing; re-dispatch, or
  publish by hand while npm/cli#9969 is open. A published version with no tag is fixed by tagging
  the commit it was published from:
  `git tag -a client/vX.Y.Z -m "Release client/vX.Y.Z" <sha> && git push origin client/vX.Y.Z`.
- **facilitator** — the tag is pushed before goreleaser, so a failure can leave the tag, and
  possibly a release and image, in place. Delete the GitHub release if one exists and cut the
  next patch number. A failed `facilitator-test` stops the run before anything is pushed. The
  `facilitator` job builds the release as a snapshot before pushing the tag, so a build that
  cannot succeed stops there. What can still fail after the tag is goreleaser's changelog and
  publish (the GitHub release, the multi-arch image and its SBOM), then the attestation steps.
- **weather** — the image is pushed before the tag, so a failure before the tag leaves at most
  an image under that version, and under `latest` for a plain release, which a re-dispatch
  overwrites. An image with no tag is fixed by tagging the commit it was built from:
  `git tag -a weather/vX.Y.Z -m "Release weather/vX.Y.Z" <sha> && git push origin weather/vX.Y.Z`.

## Credentials

npm publishing uses OIDC trusted publishing: no npm token exists in this repository. npmjs.com
matches the request against the repository and the workflow filename.

The `client` job installs with `npm ci --ignore-scripts` and restores no Actions cache, so no
dependency install script and no cache written by another job runs beside the credential.

A mismatch surfaces as `E404 package not found` or `ENEEDAUTH`, neither of which reflects the
real cause — check the workflow filename and the npm version in the job first.

A matching trusted publisher can still fail the same way. GitHub repositories created after
2026-07-15 send OIDC tokens with an immutable subject claim, and npm's token exchange rejects them
as `package not found` ([npm/cli#9969](https://github.com/npm/cli/issues/9969), open). This
repository is one, and that is the likely cause of its client publish failing with `E404` on the
`PUT`. When the publish fails, the `Show npm's OIDC exchange` step prints npm's `oidc` log lines,
which npm otherwise hides.

Until npm fixes it, an owner of `@gnoverse/x402-gno`, logged in with `npm login`, releases the
client by hand:

1. Bump `package.json` in its own PR and merge it.
2. Once CI passes on the merge commit, run `npm ci && npm publish --access public` from a clean
   checkout of it. npm asks for a 2FA code. `prepublishOnly` builds; the tests are CI's.
3. Push `client/vX.Y.Z` on that commit:
   `git tag -a client/vX.Y.Z -m "Release client/vX.Y.Z" <sha> && git push origin client/vX.Y.Z`.

A version published by hand carries no provenance.
