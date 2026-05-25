# OpenShift CI: `development-operator` overlay E2E

Scripts and CI image for the optional, on-demand Prow job
`appstudio-operator-overlay-e2e-tests` (openshift/release).

## Layout

| File | Role |
|------|------|
| `Dockerfile` | OpenShift CI `build_root` (`from: src`); UBI10 go-toolset + CLIs copied from task-runner |
| `install.sh` | Cluster bootstrap (`preview --operator-overlay`), secrets, SprayProxy |
| `run-e2e.sh` | Clone konflux-ci @ pinned ref and run conformance tests |

## Image design

Multi-stage build:

1. **Base:** `registry.access.redhat.com/ubi10/go-toolset:1.25@sha256:0a1242b10a48…` (Go + git + curl for ci-operator `/go` clone).
2. **Tools:** Copy from `quay.io/konflux-ci/task-runner:1.6.0@sha256:1abfe4e50d4e…` ([source](https://github.com/konflux-ci/task-runner)):
   - `/usr/local/bin/`: `oc`, `kubectl`, `yq`, `oras` (Go-built in task-runner)
   - `/usr/bin/jq` + `libjq` / `libonig`
   - `/usr/bin/skopeo` + a small set of linked libraries

The final image runs as UID **1001** (UBI go-toolset `default` user, primary group `root`). `/go` is
group-writable so ci-operator can clone the PR there. Build steps use `USER root` only to install
binaries under `/usr/bin` and `/usr/local/bin`.

## Local

```bash
docker build -t infra-deployments-overlay-e2e:local .
```

Run placeholders (from repo root):

```bash
./components/konflux-operator/ci/openshift-overlay-e2e/install.sh
./components/konflux-operator/ci/openshift-overlay-e2e/run-e2e.sh
```
