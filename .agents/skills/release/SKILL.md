---
name: release
description: Cut, publish, and monitor an LLM Meter release. Use when the user asks to release a new version, create a release tag, run a release dry-run, or monitor and verify release workflows and artifacts.
---

# Release

Cut and verify an LLM Meter release from `origin/main` using annotated SemVer tags and the repository's GitHub Actions workflows.

## 1. Check current release state

Refresh remote refs and tags, then inspect the latest tags, GitHub Releases, unreleased commits, `main` CI health, and open PRs:

```bash
git fetch --tags --prune origin
git tag --sort=-v:refname | head -n 10
gh release list --limit 10
gh run list --branch main --limit 5
gh pr list --state open
```

With the latest tag `<last-tag>` (if any), list commits on `origin/main` since that tag:

```bash
git log <last-tag>..origin/main --oneline
```

- If there are **no new commits** on `origin/main` since `<last-tag>`, stop and inform the user instead of re-tagging the same commit.
- Note any failing or in-progress `main` CI runs and any open PRs that look intended for the release so the user can decide whether to wait or merge them first.

## 2. Suggest the next version and confirm

Summarize the commits since `<last-tag>` and propose concrete SemVer tag options:

- If the latest tag is a pre-release such as `v0.1.0-alpha.N`: recommend `v0.1.0-alpha.(N+1)` as the primary option, alongside `v0.1.0-beta.1` and `v0.1.0`.
- If the latest tag is a stable release `vX.Y.Z`: recommend `vX.Y.(Z+1)` (patch) or `vX.(Y+1).0` (minor) — or `v(X+1).0.0` for breaking changes — based on the unreleased changes.
- Explain release classification: `.github/workflows/release.yml` automatically marks tags containing `-` before any `+` metadata (`${RELEASE_TAG%%+*} == *-*`) as **Pre-release**, while plain `vX.Y.Z` tags publish as **Latest / stable**. Stick to clean SemVer (`vX.Y.Z` or `vX.Y.Z-<prerelease>`) so `docker/metadata-action` publishes matching GHCR tags.

Ask the user to confirm:
1. The exact tag to release.
2. Whether to run a `workflow_dispatch` dry-run first.

Use the harness's structured question tool (`ask_question` or equivalent), falling back to plain text if unavailable. Skip asking only when the user has already named the exact tag and explicitly confirmed proceeding.

## 3. Optional dry-run

If the user requested a dry-run, dispatch `.github/workflows/release.yml` with a `-dryrun` label (this builds all binaries, the macOS DMG, and `checksums.txt`, and uploads a `release-dryrun` workflow artifact without creating a GitHub Release):

```bash
gh workflow run release.yml -f tag=<tag>-dryrun
```

Locate the dispatched run and watch it to completion before creating the real tag:

```bash
gh run list --workflow release.yml --event workflow_dispatch --limit 5
gh run watch <run-id> --exit-status
```

If the dry-run fails, inspect with `gh run view <run-id> --log-failed`, report the root cause, and stop.

## 4. Create and push the annotated tag on `origin/main`

Validate the tag format (`^v[0-9][A-Za-z0-9.+-]*$`, matching `.github/workflows/release.yml`) and verify it does not already exist locally or on `origin`:

```bash
[[ "<tag>" =~ ^v[0-9][A-Za-z0-9.+-]*$ ]] || { echo "Invalid tag format" >&2; exit 1; }
git rev-parse -q --verify "refs/tags/<tag>" >/dev/null && { echo "Tag already exists locally" >&2; exit 1; }
[[ -z "$(git ls-remote --tags origin "refs/tags/<tag>")" ]] || { echo "Tag already exists on origin" >&2; exit 1; }
```

Create the annotated tag directly on `origin/main` (works cleanly from any branch or worktree) and push it:

```bash
git tag -a <tag> origin/main -m "LLM Meter <tag>" && git push origin <tag>
```

## 5. Monitor all release-related CI workflows

Pushing a `v[0-9]*` tag triggers three workflows. List their runs (wait briefly once if GitHub has not queued all three yet):

```bash
gh run list --branch <tag> --limit 10
```

Expect all three workflows for `<tag>`:
- **`Release`** (`.github/workflows/release.yml`): cross-compiled Go binaries (`llm-meter` with `-tags embedui` and `llm-meter-relay`) for `linux/{amd64,arm64}` and `darwin/{amd64,arm64}`, macOS `.dmg`, `checksums.txt`, and GitHub Release creation.
- **`CI`** (`.github/workflows/ci.yml`): web + Go test/vet/build checks, relay container smoke test, and multi-arch (`linux/amd64,linux/arm64`) GHCR publish (`ghcr.io/mishankov/llm-meter:<tag>` and `ghcr.io/mishankov/llm-meter-relay:<tag>` via `.github/workflows/docker-image.yml`).
- **`macOS relay`** (`.github/workflows/desktop.yml`): macOS desktop Wails packaging, Go tests/vet, and Playwright WebKit tests.

Watch each run to completion with `gh run watch` (never write manual `sleep` polling loops):

```bash
gh run watch <run-id> --exit-status
```

If any run fails:
- Inspect the failure with `gh run view <run-id> --log-failed`.
- Report the failing workflow, job/step, and exact root cause to the user.
- Ask the user before deleting any tag/release or re-tagging.

## 6. Verify published release and container images

Verify the GitHub Release metadata and all **10** expected assets (8 `.tar.gz`, 1 `.dmg`, and `checksums.txt`):

```bash
gh release view <tag> --json tagName,isPrerelease,url,assets
```

Confirm:
- `isPrerelease` matches the tag (`true` when `<tag>` contains `-` before any `+`, `false` for stable `vX.Y.Z`).
- All 10 assets are present:
  - `llm-meter_<tag>_{linux,darwin}_{amd64,arm64}.tar.gz` (4 archives)
  - `llm-meter-relay_<tag>_{linux,darwin}_{amd64,arm64}.tar.gz` (4 archives)
  - `LLM-Meter-Relay_<tag>_darwin_<arch>.dmg` (1 DMG, `darwin_arm64` on `macos-latest`)
  - `checksums.txt`

Verify anonymous GHCR pullability and multi-arch manifests (`linux/amd64`, `linux/arm64`) for both images without relying on cached local credentials:

```bash
for img in llm-meter llm-meter-relay; do
  token=$(curl -fsSL "https://ghcr.io/token?service=ghcr.io&scope=repository:mishankov/${img}:pull" | jq -r .token)
  curl -fsSL \
    -H "Authorization: Bearer ${token}" \
    -H "Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json" \
    "https://ghcr.io/v2/mishankov/${img}/manifests/<tag>" \
    | jq -r --arg img "$img" '"\($img): " + ([.manifests[]?.platform | select(.os != "unknown") | "\(.os)/\(.architecture)"] | join(", "))'
done
```

If the user asks for full post-release verification, offer/run a clean-room smoke test in a temporary directory: download the platform tarball and `checksums.txt`, verify with `shasum -a 256 --ignore-missing -c checksums.txt` (macOS) or `sha256sum -c --ignore-missing checksums.txt` (Linux), and confirm the extracted binaries and/or pulled GHCR images start cleanly.

## 7. Final report

Report back concisely with:
- GitHub Release URL
- Tag and target commit SHA (`git rev-parse <tag>^{commit}`)
- Release status (Pre-release vs. Latest / stable)
- Workflow run URLs and outcomes (`Release`, `CI`, `macOS relay`)
- Published Docker image tags (`ghcr.io/mishankov/llm-meter:<tag>`, `ghcr.io/mishankov/llm-meter-relay:<tag>`) and verified platforms
- Asset count and summary (10 assets: 8 `.tar.gz`, 1 `.dmg`, `checksums.txt`)
