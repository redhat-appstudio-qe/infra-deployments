# Konflux Operator Component

This directory holds the **manifests** used by Argo CD on OpenShift to install the
[Konflux operator](https://github.com/konflux-ci/konflux-ci) and to define a default
`Konflux` custom resource (instance configuration). Cluster operators and maintainers
edit these files; reviewers use this file to understand layout and promotion rules.

The operator install bundle is consumed as **plain Kubernetes manifests** (CRDs, RBAC,
Deployment, and so on), not as OLM `Subscription` / `ClusterServiceVersion` objects.

## Directory layout (ring-based)

Deployments follow a **ring-based Kustomize structure** with Kargo-driven promotions:

| Tier | Path | Purpose |
|------|------|---------|
| 1 | `rings/ring-N/base/` | Upstream ref, image pin, `Konflux` CR, CR overlay patches, and release config. Each ring owns its full configuration. |
| 2 | `rings/ring-N/<cluster>/` | Per-cluster overlays that reference `../base`. |

Example (`ring-0`):

```text
rings/
  ring-0/
    base/
      kustomization.yaml          # resources: [invariant], components: cr/..., patches: [release-config.yaml]
      release-config.yaml         # environment-specific spec (defaultTenant, certManager, etc.)
      cr/                         # Konflux CR overlay patches (ring-owned)
        build/
        konflux-ui/
        image-controller/
        ...
      invariant/
        kustomization.yaml        # remote operator + images + konflux.yaml
        konflux.yaml              # minimal Konflux CR shell
```

## What gets promoted across rings

Only the **invariant content** in `rings/ring-N/base/invariant/` is promoted by Kargo:
- Upstream remote ref (the `?ref=` in `resources`)
- Image tags (the `images` block)
- Base `Konflux` CR (`konflux.yaml`)

The `cr/` overlay patches and `release-config.yaml` live inside each ring's `base/`
directory and are **ring-specific**. When adding a new ring, copy or adapt them as needed.

## Promoting to another ring

1. Kargo promotes the invariant content (upstream ref, image pin) from one ring's
   `base/invariant/` to the next ring's `base/invariant/`.
2. `cr/` overlay patches and `release-config.yaml` are ring-local — adapt per ring as needed.
3. **Rollback** is a normal Git operation (`git revert`, or restore the invariant
   from an earlier revision).

Adding a new ring: create `rings/ring-N/base/` with the same shape, seed
the invariant from the ring you trust, and copy or adapt `cr/` patches and `release-config.yaml`.

## Preview script and `Konflux` readiness

`hack/preview.sh` supports **`--operator-overlay`** (OpenShift preview using the
`development-operator` Argo overlay). **By default it does not wait** for the cluster
`Konflux` object `konflux` to become ready, so preview can finish after Argo CD sync
while the operator and instance are still converging.

To **gate** preview on a healthy instance (same checks as
`konflux-ci/scripts/deploy-local.sh`), set **`PREVIEW_WAIT_KONFLUX_CR_READY=true`**
for that run.

## Applying manifests locally (optional)

From the repository root, after logging in to a cluster:

`kubectl apply -k components/konflux-operator/rings/ring-0/base`

To sync **only** the operator controller and CRDs without applying the `Konflux`
instance, temporarily remove the CR inputs from `rings/ring-0/base/invariant/kustomization.yaml`
(for example `konflux.yaml`), apply, then restore those lines when you want the instance.

## Konflux CR ownership

- **`rings/ring-N/base/invariant/konflux.yaml`** — minimal `Konflux` object.
- **`rings/ring-N/base/release-config.yaml`** — environment-specific `spec`
  (defaultTenant, certManager, internalRegistry, etc.).
- **`rings/ring-N/base/cr/*/OWNERS`** — team ownership for overlay fragments under each
  subdirectory.
