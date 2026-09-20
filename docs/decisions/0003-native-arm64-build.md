# 0003 - Native arm64 GitHub runner instead of QEMU emulation

- Status: Accepted
- Date: 2026-09-20
- Deciders: @bibigon14

## Context

The operator has to ship as a multi-arch image because the homelab
runs on `linux/arm64` (Raspberry Pi 5) while contributors and CI runners
are `linux/amd64`. The kubebuilder-scaffolded workflow used
`docker/setup-qemu-action` and passed both platforms to
`docker/build-push-action` in a single job.

Wall-clock breakdown of the original setup:

- `lint` + `test` (amd64 native): ~3 minutes
- `build` job: ~25 minutes total, dominated by ~22 minutes of arm64
  compilation running under QEMU on the amd64 runner

Every push waited 25 minutes for a new image, which made the
edit-deploy-observe loop painful. The homelab uses `imagePullPolicy:
Always` on tag `:latest`, so a `kubectl rollout restart` is required
after each build regardless of ArgoCD sync - and there is no image to
restart to until CI finishes.

## Decision drivers

- Cut the build time enough that iterating on the operator is not
  gated by CI.
- Do not introduce a private runner fleet or paid tier.
- Keep the Dockerfile stock kubebuilder so the repo remains familiar.

## Options considered

### Option A: Cross-compile in Go, pack arm64 binary into arm64 image

Set `GOOS=linux GOARCH=arm64` in the build stage of the Dockerfile
and let Go's native cross-compiler produce both binaries on the amd64
runner. Skip QEMU entirely. Fast (~3 minutes total), but requires
Dockerfile changes and drifts from the kubebuilder scaffold.

### Option B: Native arm64 runner in a matrix

Since January 2026, GitHub Actions offers `ubuntu-24.04-arm` as a
free runner for public repositories. Split the build into a matrix:
amd64 leg on `ubuntu-latest`, arm64 leg on `ubuntu-24.04-arm`, each
producing its own digest via `push-by-digest`. A final `merge` job
stitches the two digests into a single multi-arch manifest under the
computed tags.

### Option C: Self-hosted arm64 runner on the Pi

Free and native, but adds a moving part (the runner service) that has
to be maintained and monitored, and gives GitHub's runner-execution
context write access to the homelab network. Rejected for the security
and operational cost.

## Decision

**Option B.**

Workflow shape:

```yaml
build:
  strategy:
    matrix:
      platform:
        - {name: amd64, runner: ubuntu-latest,      docker: linux/amd64}
        - {name: arm64, runner: ubuntu-24.04-arm,   docker: linux/arm64}
  runs-on: ${{ matrix.platform.runner }}
  # ... push-by-digest per arch, upload digest as artifact

merge:
  needs: [build]
  runs-on: ubuntu-latest
  # ... download both digests, docker buildx imagetools create ...
```

GHA cache is scoped per-arch (`cache-to: type=gha,scope=${{ matrix.platform.name }}`)
so the two legs do not evict each other.

## Consequences

Positive:

- Build time drops from ~25 minutes to ~3 minutes end-to-end. The two
  build legs run in parallel; the merge job is ~15 seconds.
- No changes to the Dockerfile - stays identical to the kubebuilder
  scaffold.
- No cross-compilation footguns (Go stdlib and CGo interactions
  around timezone data, netcgo, etc. all continue to build natively
  for their target).

Negative:

- The workflow is more verbose: two jobs + a merge job instead of one
  build job. Trade-off is acceptable given the time saved.
- `ubuntu-24.04-arm` is a relatively new runner image. If GitHub
  changes its availability or pricing for public repos, we would need
  to fall back to Option A (Go cross-compile). The migration would be
  contained to `.github/workflows/ci.yml` and `Dockerfile`.
- Docker Hub is a hard dependency of the `docker/setup-buildx-action`
  step (pulls `moby/buildkit`). Transient Docker Hub failures cause
  the merge job to fail; retry via `gh run rerun --failed` completes in
  seconds because the per-arch build digests are already in GHCR.
