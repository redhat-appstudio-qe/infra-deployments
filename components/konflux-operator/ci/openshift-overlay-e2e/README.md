# OpenShift CI: `development-operator` overlay E2E

Scripts for the optional, on-demand Prow job
`appstudio-operator-overlay-e2e-tests` (openshift/release).

Legacy `appstudio-e2e-tests` (`development` overlay) is unchanged and uses separate
step refs (`konflux-ci-install-konflux`, `redhat-appstudio-conformance-tests`).

## Layout

| File | Role |
|------|------|
| `ci-common.sh` | Shared cluster login (`oc config` + `oc login`, no `yq`) and git credentials |
| `install.sh` | Cluster bootstrap (`preview --operator-overlay`), secrets, SprayProxy |
| `run-e2e.sh` | Clone konflux-ci @ pinned ref and run conformance tests |

## CI flow (both steps use the same pattern)

1. Step runs in a fixed image (`from_image` in openshift/release).
2. Shared entrypoint `redhat-appstudio-operator-overlay-install-commands.sh` clones `infra-deployments`, merges the PR when applicable, and calls `ci-common.sh`.
3. The e2e step sets `OVERLAY_E2E_SCRIPT_NAME=run-e2e.sh` and sources that same entrypoint.
4. `install.sh` or `run-e2e.sh` runs the phase-specific logic.

| Step | Image |
|------|-------|
| Install | `quay.io/konflux-ci/task-runner:1.6.0` |
| E2E | `openshift/release:rhel-9-release-golang-1.25-openshift-4.21` |

Both refs set `cli: latest` so `oc` is available in-step.

## Local

Run from an `infra-deployments` checkout (after setting `KUBECONFIG`, `GITHUB_TOKEN`, etc.):

```bash
./components/konflux-operator/ci/openshift-overlay-e2e/install.sh
./components/konflux-operator/ci/openshift-overlay-e2e/run-e2e.sh
```
