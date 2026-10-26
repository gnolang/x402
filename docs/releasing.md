# Releasing

Three components, three independent version lines. Releasing one does not touch the others.

| Component | Tag | Produces |
|---|---|---|
| `facilitator` | `vX.Y.Z` | archives + GitHub release + `ghcr.io/gnolang/gnofacilitator`, attested. Also the Go module version. |
| `client` | `client/vX.Y.Z` | `@gnoverse/x402-gno` on npm, with provenance |
| `weather` | `weather/vX.Y.Z` | `ghcr.io/gnolang/x402-weather`, the demo seller's image for amd64 and arm64, attested; `latest` follows plain releases only |

The Go mechanisms, `server/exact` and `client/exact`, are packages of the Go module, so they
release with the facilitator's `vX.Y.Z`, and so does the weather example's Go buyer,
`examples/weather/x402-gno`. `client/vX.Y.Z` releases the npm package alone.

All three run from the manual `release.yml` workflow. Nothing releases on push or merge.

## Release the facilitator

The facilitator's version is its git tag alone, so the release starts with a PR that moves the
pins to the new version.

1. In a PR merged before the release, move the `vX.Y.Z` that the
   `go run github.com/gnolang/x402/examples/weather/x402-gno@…` commands pin in
   `examples/weather/paywall.html`, `examples/weather/README.md` and `client/README.md` to the new
   version. Move the `go install` line beside them and the tag in the weather page's and the
   weather README's links to `x402-gno` too, so the buyer a reader is told to read is the one the
   commands run.
2. Dispatch the release, with `<version>` as `X.Y.Z`:

   ```sh
   gh workflow run release.yml --ref main -f component=facilitator -f version=<version>
   ```

The tag does not exist until the run pushes it.

The run has two jobs. `facilitator-test` holds a read-only token and runs `go test ./...` and the
e2e, after `npm ci` installs exactly the lockfile. Only when it passes does `facilitator` start:
it validates the version, builds the release as a snapshot, pushes the tag, publishes and attests.
The tests run code from every Go and npm dependency, install scripts included, so they never run
in the job that can push to the repository and to ghcr.io and mint OIDC tokens. That job restores
no Actions cache either, because the jobs that run dependency code write it.

## Release the client

The published version comes from `package.json`, so the release starts with a PR that bumps it.

1. On a new branch, bump the version, with `<version>` as `X.Y.Z`:

   ```sh
   npm version --no-git-tag-version <version>
   ```

2. In the same change, move the `client/vX.Y.Z` that the `npx github:gnolang/x402#…` commands pin
   to the new version in `examples/weather/paywall.html`, `examples/weather/README.md` and
   `client/README.md`. Move the tag in the weather page's and the weather README's links to
   `buy.mjs` too, so the script a reader is told to read is the one the commands run.
3. Commit the change:

   ```sh
   git commit -am "chore(release): bump the client to <version>"
   ```

4. Push the branch, open a PR and merge it to `main`.
5. Dispatch the release:

   ```sh
   gh workflow run release.yml --ref main -f component=client -f version=<version>
   ```

The tag does not exist until the run pushes it, and from then on the pinned commands run the buyer
that release carries.

The workflow refuses the dispatch if `package.json` disagrees with `version`, or if that
version is already on the registry. When the `npm` environment lists required reviewers, the
publish waits until one of them approves it under **Review deployments** on the run's page.

## Release the weather image

The version lives in the tag alone, so nothing needs a bump. Dispatch the release, with
`<version>` as `X.Y.Z`:

```sh
gh workflow run release.yml --ref main -f component=weather -f version=<version>
```

The run's summary names the image by digest, which is what a deployment pins. The image is built
from `docker/weather.Dockerfile`, which compiles the seller itself, so
`docker build -f docker/weather.Dockerfile .` from the repository root builds the same thing
locally.

## Rules

These hold for every release:

- **Release from `main` only.** The workflow refuses any other ref.
- **Never delete a `vX.Y.Z` tag.** It is a Go module version; once fetched, `sum.golang.org`
  binds it to that commit permanently, and re-cutting it breaks every downstream build. Cut
  the next patch number instead. `client/vX.Y.Z` and `weather/vX.Y.Z` have no such constraint.
- **Use `npm version`, not a hand edit.** It updates `package-lock.json` too, which `npm ci`
  does not check.
- **Renaming `release.yml` or the `npm` environment breaks npm publishing** until npm's
  trusted publisher is updated to match. A renamed environment also needs its own `main`
  branch rule on GitHub.

## If a release fails

What a failure leaves behind depends on the component:

- **client**: publish runs before the tag, so a failed publish leaves nothing; re-dispatch. A
  published version with no tag is fixed by tagging the commit it was published from:
  `git tag -a client/v<version> -m "Release client/v<version>" <sha> && git push origin client/v<version>`.
- **facilitator**: the tag is pushed before goreleaser, so a failure can leave the tag, and
  possibly a release and image, in place. Delete the GitHub release if one exists, merge a PR
  that moves the pins to the next patch number, and cut that release. A failed
  `facilitator-test` stops the run before anything is pushed, and the snapshot build stops a
  build that cannot succeed before the tag. What can still fail after the tag is goreleaser's
  changelog and publish (the GitHub release, the multi-arch image and its SBOM), then the
  attestation steps.
- **weather**: the image is pushed before the tag, so a failure before the tag leaves at most
  an image under that version, and under `latest` for a plain release, which a re-dispatch
  overwrites. An image with no tag is fixed by tagging the commit it was built from:
  `git tag -a weather/v<version> -m "Release weather/v<version>" <sha> && git push origin weather/v<version>`.

## Credentials

npm publishing uses OIDC trusted publishing: no npm token exists in this repository. npmjs.com
matches the request against the repository, the workflow filename and the GitHub environment,
and it compares each field exactly and case-sensitively.

The `client` job installs with `npm ci --ignore-scripts` and restores no Actions cache, so no
dependency install script and no cache written by another job runs beside the credential.

It runs in the `npm` environment, which admits `main` only. That rule lives in the repository
settings, where a copy of `release.yml` edited on another branch cannot change it. Set the
environment up on GitHub first: binding npm to it while `release.yml` on `main` does not name
it refuses every publish.

1. On github.com, as a repository admin, open **Settings → Environments**. A run that names a
   missing environment creates it with no rules, so open `npm` if it is listed; otherwise
   click **New environment**, name it `npm`, and click **Configure environment**.
2. Under **Deployment branches and tags**, choose **Selected branches and tags**, add a
   **Branch** rule for `main`, and save.
3. Optional: under **Required reviewers**, list who approves a client publish. Every client
   run then waits for one of them; **Prevent self-review** makes that a second person.
4. Once `release.yml` on `main` names the environment, on npmjs.com, as an owner of
   `@gnoverse/x402-gno`, open the package's **Settings → Trusted publishing**, edit the GitHub
   Actions publisher and set **Environment name** to `npm`. Where the page offers no edit,
   delete the publisher and add it again with the same organization (`gnolang`), repository
   (`x402`), workflow filename (`release.yml`) and allowed actions, plus the environment.

A mismatch surfaces as `E404 package not found` or `ENEEDAUTH`, neither of which reflects the
real cause: check the organization and repository, the workflow filename, the environment name
and the npm version in the job first. When the publish fails, the `Show npm's OIDC exchange`
step prints npm's `oidc` log lines, which npm otherwise hides.
