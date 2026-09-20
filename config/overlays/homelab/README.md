# Homelab overlay

Opinionated deployment for the author's Raspberry Pi k3s homelab.

Differences from `config/default`:

| Aspect           | `config/default`               | `config/overlays/homelab`      |
|------------------|--------------------------------|--------------------------------|
| Metrics endpoint | HTTPS `:8443` via rbac-proxy   | Plain HTTP `:8080`             |
| Metrics service  | ClusterIP                      | NodePort `30190`               |
| Image            | `controller:latest`            | `ghcr.io/bibigon14/...:latest` |

## Apply

    kustomize build config/overlays/homelab | kubectl apply -f -

## Why insecure metrics

Homelab Prometheus runs on a separate node without a service account
token or TLS trust to the operator. Rather than provision cert-manager
just for one scrape target, this overlay disables TLS. Production
should keep `config/default`, which uses controller-runtime's built-in
authn/authz-protected metrics endpoint.
