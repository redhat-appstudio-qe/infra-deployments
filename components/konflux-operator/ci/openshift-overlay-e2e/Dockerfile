# CI build-root for appstudio-operator-overlay-e2e-tests (OpenShift CI `from: src`).
# Go toolchain: UBI10 go-toolset (digest-pinned). CLIs: copied from konflux-ci/task-runner (same OS major).
# https://github.com/konflux-ci/task-runner

FROM quay.io/konflux-ci/task-runner:1.6.0@sha256:1abfe4e50d4e961d0fd9790202565f93ee650fe8dfc50932c94989acba10485f AS runner

FROM registry.access.redhat.com/ubi10/go-toolset:1.25@sha256:0a1242b10a483946adcf3e35e18f5a19996869468f73deff2c5ce451ce0fa6bc

# Root only to install system-wide CLIs; CI steps run as the go-toolset default user (1001).
USER root

# Go-built task-runner binaries (versions in task-runner Installed-Software.md).
COPY --from=runner /usr/local/bin/oc /usr/local/bin/kubectl /usr/local/bin/yq /usr/local/bin/oras /usr/local/bin/

# RPM binaries from the runner rootfs (same UBI10 ABI as this base).
COPY --from=runner /usr/bin/jq /usr/bin/jq
COPY --from=runner /lib64/libjq.so.1 /lib64/libonig.so.5 /lib64/
COPY --from=runner /usr/bin/skopeo /usr/bin/skopeo
COPY --from=runner /lib64/libgpgme.so.11 /lib64/libassuan.so.0 /lib64/libgpg-error.so.0 /lib64/libsqlite3.so.0 /lib64/libsubid.so.4 /lib64/

ENV HOME=/go
WORKDIR /go/src/github.com/redhat-appstudio/infra-deployments
RUN chmod -R g+w /go

USER 1001
