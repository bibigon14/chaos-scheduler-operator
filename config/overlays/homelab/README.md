# Homelab overlay

Opinionated deployment for the author's Raspberry Pi k3s homelab.

Differences from `config/default`:

| Aspect           | `config/default`               | `config/overlays/homelab`      |
|------------------|--------------------------------|--------------------------------|
| Metrics endpoint | HTTPS `:8443` via rbac-proxy   | Plain HTTP `:8080`             |
| Metrics service  | ClusterIP                      | NodePort `30190`               |
| Image            | `controller:latest`            | `ghcr.io/bibigon14/...:latest` |

## Deploy

### GitOps (preferred)

The overlay is wired to ArgoCD. Apply the Application manifest once and
ArgoCD will keep the cluster in sync with `main`:

    kubectl apply -f ../../../argocd/application.yaml

See [`argocd/application.yaml`](../../../argocd/application.yaml) for the
sync policy (automated prune + self-heal + ServerSideApply).

### Manual (fallback)

For clusters without ArgoCD, or for quick smoke tests:

    kustomize build config/overlays/homelab | kubectl apply -f -

## Why insecure metrics

Homelab Prometheus runs on a separate node without a service account
token or TLS trust to the operator. Rather than provision cert-manager
just for one scrape target, this overlay disables TLS. Production
should keep `config/default`, which uses controller-runtime's built-in
authn/authz-protected metrics endpoint.
