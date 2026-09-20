# 0002 - ArgoCD Application manifest lives in the operator repo

- Status: Accepted
- Date: 2026-09-19
- Deciders: @bibigon14

## Context

The homelab cluster already runs ArgoCD and manages most workloads
through it (`homelab-k3s` repo holds ~10 Applications for things like
`chaos-monkey`, `wc2026bot`, `river-bot`, `alertmanager-telegram-bridge`).
The operator was initially deployed with `kubectl apply -k` from a
laptop, which is fine for smoke tests but drifts from how the rest of
the homelab is managed.

Two natural places for the ArgoCD `Application` CR:

1. In the existing `homelab-k3s` repo, next to the other Applications.
2. In this operator repo, under a new `argocd/` directory.

## Decision drivers

- Consistency with the rest of the homelab (points to option 1).
- Portfolio self-containment: a reader of this repo should see how it
  is meant to be deployed without having to find a second repo (points
  to option 2).
- Blast radius: an accidental change to `homelab-k3s` affects every
  ArgoCD-managed workload, not just this operator (points to option 2).

## Options considered

### Option A: Application manifest in `homelab-k3s`

Matches the existing pattern. Every workload's Application lives in
one central repo, easy to see everything ArgoCD manages by looking at
one directory. Downside: the operator repo becomes a passive artifact
that requires reading a second repo to understand its production path.

### Option B: Application manifest in the operator repo

Ship `argocd/application.yaml` alongside the code and manifests it
deploys. `kubectl apply -f argocd/application.yaml` bootstraps
ArgoCD-managed deployment in one command. The manifest points at
`config/overlays/homelab` in this same repo at `HEAD`, so a change
here reaches the cluster through ArgoCD reconciliation.

## Decision

**Option B.**

The manifest is at `argocd/application.yaml` with:

- `spec.source.path: config/overlays/homelab`
- `spec.source.targetRevision: HEAD`
- `spec.syncPolicy.automated: {prune: true, selfHeal: true}`
- `spec.syncPolicy.syncOptions: [CreateNamespace=true, ServerSideApply=true]`

`ServerSideApply` matters here because the manager's ClusterRoles
carry enough rules to push the standard `kubectl.kubernetes.io/last-applied-configuration`
annotation past the 256 KB limit over time. Server-side apply avoids
that annotation entirely.

## Consequences

Positive:

- Cloning the repo and running `kubectl apply -f argocd/application.yaml`
  gets a reader from zero to a self-healing deployment.
- The Application manifest is versioned with the code it deploys.
  Breaking changes to the overlay and to the Application land in the
  same commit.
- Deletion of the Application (via the finalizer) tears down the
  operator cleanly.

Negative:

- Diverges from the "all Applications live in `homelab-k3s`" pattern.
  Mitigated by the fact that portfolio projects each carry their own
  Application anyway - `homelab-k3s` holds the ones that pre-date this
  convention.
- A reader who does not know ArgoCD sees an unfamiliar `apiVersion:
  argoproj.io/v1alpha1` in the tree. Mitigated by the file's header
  comment and the top-level README's Deploy section pointing at it.
