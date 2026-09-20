# 0001 - Two kustomize flavors: production default and homelab overlay

- Status: Accepted
- Date: 2026-09-19
- Deciders: @bibigon14

## Context

The operator ships with the kubebuilder-scaffolded `config/default`,
which exposes the manager's metrics endpoint over HTTPS on `:8443`
behind `kube-rbac-proxy` and uses a `ClusterIP` service. That is a
sensible production posture: metrics are authn/authz-protected and
only reachable from inside the cluster.

The author's homelab, where the operator actually runs, does not meet
those assumptions. Prometheus lives on the Pi host itself, not inside
k3s, and scrapes over the node network. It has no service account
token or TLS trust to the operator's SA. To scrape the default
setup we would have to provision cert-manager, mint a ServiceAccount
token, and wire both into the Prometheus config for a single target.

At the same time, the portfolio value of `config/default` is that it
represents what a production deployment should look like. Losing that
by permanently disabling TLS in the base overlay would be a signal
that the author does not understand the security posture.

## Decision drivers

- Keep the production-grade defaults intact for portfolio credibility.
- Do not require cert-manager or SA-token plumbing for the homelab.
- Make it obvious to a reader which flavor represents which context.
- Avoid a runtime-only patch that drifts from the committed manifests.

## Options considered

### Option A: Modify `config/default` to be insecure

Simplest change, but the repo would then tell a reader "the author
does not know how to configure secure metrics." Rejected.

### Option B: Overlay-based split

Keep `config/default` untouched. Add `config/overlays/homelab` that
patches the deployment args (`--metrics-secure=false`,
`--metrics-bind-address=:8080`) and the service (`type: NodePort`,
`nodePort: 30190`). Document both flavors in the overlay README.

### Option C: Provision cert-manager and secure the homelab scrape

Most correct answer. Rejected for scope: cert-manager is one operator
too many for the current homelab, and the exercise here is about the
chaos-scheduler itself, not TLS infrastructure.

## Decision

**Option B.**

Layout:

```
config/
├── default/            # kubebuilder baseline, HTTPS :8443, ClusterIP
└── overlays/
    └── homelab/
        ├── kustomization.yaml
        ├── manager_insecure_metrics_patch.yaml
        ├── metrics_service_nodeport_patch.yaml
        └── README.md    # explains why the deviation exists
```

The homelab overlay uses `patches:` with JSON-patch `replace`
operations (not `patchesStrategicMerge`) for the deployment args, so
the final ordering of args is deterministic regardless of what other
patches touch the deployment.

## Consequences

Positive:

- `kubectl apply -k config/default` continues to be a valid, secure
  install path for anyone reading the repo.
- The homelab overlay's `README.md` documents both flavors in a table
  and explains why insecure metrics are acceptable in that one context.
- Homelab deploy is one command: `kubectl apply -k config/overlays/homelab`.

Negative:

- Two flavors is more surface area to keep in sync as the manager
  spec evolves. Mitigated by the overlay only replacing a small,
  well-defined subset (args + service ports).
- A reader has to notice the overlay exists to understand how the
  homelab actually runs. Mitigated by calling it out in the top-level
  README's Deploy section.
