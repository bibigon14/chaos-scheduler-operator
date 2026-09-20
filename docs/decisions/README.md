# Architecture decision records

This directory holds decision records for choices that would otherwise
live only in commit messages or in someone's head. The bar is "would a
new contributor look at this and ask *why*?" - if yes, it goes here.

Format follows [MADR](https://adr.github.io/madr/) loosely: context,
options considered, decision, consequences. Kept short on purpose.
Records are numbered and immutable once accepted; supersede by adding
a new record and marking the old one `Superseded by NNNN`.

## Index

- [0001](0001-two-flavor-deploy.md) - Two kustomize flavors: production default and homelab overlay
- [0002](0002-argocd-application-in-repo.md) - ArgoCD Application manifest lives in the operator repo
- [0003](0003-native-arm64-build.md) - Native arm64 GitHub runner instead of QEMU emulation
