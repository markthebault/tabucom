# Release Process

Tabucom uses GitHub Actions, Conventional Commits, and Release Please to automate version numbers, changelogs, Git tags, GitHub Releases, container images, and release binaries.

## Overview

There are four automation layers:

1. **CI**: runs formatting, tests, vet, JSON validation, and a binary build on pull requests and pushes to `main`.
2. **Container publishing**: builds and pushes `ghcr.io/markthebault/tabucom:main` plus a `main-<short-sha>` tag on every successful push to `main`.
3. **Release Please**: watches commits on `main`, computes the next version, and opens or updates a release pull request.
4. **Release binaries**: builds platform binaries and uploads them to the GitHub Release.

## Daily Development Flow

Use normal feature branches and pull requests.

```sh
git checkout -b feature/my-change
# edit files
git commit -m "feat: add publish option"
git push origin feature/my-change
```

Open a pull request and merge it into `main` after CI is green. After the merge, Release Please runs automatically on `main`.

## Commit Message Rules

Release Please reads commit messages to decide whether a release is needed and what version number to use.

Use Conventional Commits:

```text
fix: reject duplicate archive paths
feat: add S3 storage backend
feat!: change publish response contract
```

Version bump rules:

| Commit message | Version bump | Example |
|---|---:|---|
| `fix: ...` | patch | `v0.1.0` -> `v0.1.1` |
| `feat: ...` | minor | `v0.1.1` -> `v0.2.0` |
| `feat!: ...` | major | `v1.2.3` -> `v2.0.0` |
| `BREAKING CHANGE:` in commit body | major | `v1.2.3` -> `v2.0.0` |

Non-release commit types do not normally create a release:

```text
docs: update publish examples
ci: update workflow
chore: clean generated files
test: add archive traversal coverage
refactor: simplify validation
```

## How To Publish A Release

To publish a release, merge the Release Please pull request.

After that merge, Release Please automatically creates:

- a Git tag, for example `v0.2.0`;
- a GitHub Release;
- release notes based on the changelog.

Then the Release Please workflow builds and uploads these assets:

```text
tabucom_vX.Y.Z_darwin_amd64.tar.gz
tabucom_vX.Y.Z_darwin_arm64.tar.gz
tabucom_vX.Y.Z_linux_amd64.tar.gz
tabucom_vX.Y.Z_linux_arm64.tar.gz
tabucom_vX.Y.Z_windows_amd64.zip
checksums.txt
```

The same Release Please workflow also publishes the GHCR image from the Release Please tag. For a release tag such as `v0.2.0`, the image receives:

```text
ghcr.io/markthebault/tabucom:v0.2.0
ghcr.io/markthebault/tabucom:0.2.0
ghcr.io/markthebault/tabucom:0.2
ghcr.io/markthebault/tabucom:latest
```

Use GitHub releases for immutable binary downloads and semver GHCR tags for container deployment. Use `main` or `main-<short-sha>` only for testing unreleased code.

The separate `Release Binaries` workflow is a manual backfill path. Use it with
`workflow_dispatch` and an existing tag, such as `v0.2.0`, if release assets or
container tags need to be rebuilt after a workflow fix.

## Snapshot Binaries On Main

Every push to `main` runs CI. If CI passes, the workflow builds snapshot binaries and stores them as temporary GitHub Actions artifacts.

Snapshot artifacts are useful for testing the current `main` branch before publishing a release. Snapshot artifacts are not official releases and expire after the retention period configured in `.github/workflows/ci.yml`.

## Should I Use GitHub's Manual Release UI?

Do not use GitHub's manual Release UI for normal releases.

Normal releases must go through Release Please so that:

- version numbers are computed from commits;
- `CHANGELOG.md` stays correct;
- tags are created consistently;
- binaries are uploaded by automation.

Use GitHub's manual Release UI only for emergency edits to an already-created release description.

## Local Checks Before Opening A Pull Request

Run:

```sh
gofmt -w ./cmd ./internal
go test ./...
go vet ./...
python3 -m json.tool internal/server/web/openapi.json >/dev/null
python3 -m json.tool internal/server/web/.well-known/agent.json >/dev/null
go build ./cmd/tabucom
```

Do not commit generated release archives from `dist/`.

## Relevant Files

```text
.github/workflows/ci.yml
.github/workflows/release-please.yml
.github/workflows/release.yml
release-please-config.json
.release-please-manifest.json
scripts/build-release.sh
```
