#!/usr/bin/env bash
# Shared OpenShift CI helpers for operator-overlay install and e2e steps.
# Expects GITHUB_TOKEN and INFRA_DEPLOYMENTS_ROOT to be set by the step entrypoint.

set -euo pipefail

ci_prepare_cluster_access() {
  local openshift_api openshift_password cluster_name

  # Normalize pool kubeconfig TLS (same outcome as yq in konflux-ci install steps).
  # Uses only oc so install (task-runner) and e2e (go-toolset) behave identically.
  cluster_name="$(oc config view --minify -o jsonpath='{.clusters[0].name}')"
  openshift_api="$(oc config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
  oc config set-cluster "${cluster_name}" --insecure-skip-tls-verify=true
  oc config unset "clusters.${cluster_name}.certificate-authority-data" 2>/dev/null || true

  if [[ -s "${KUBEADMIN_PASSWORD_FILE:-}" ]]; then
    openshift_password="$(cat "${KUBEADMIN_PASSWORD_FILE}")"
  elif [[ -s "${SHARED_DIR}/kubeadmin-password" ]]; then
    openshift_password="$(cat "${SHARED_DIR}/kubeadmin-password")"
  else
    echo "Kubeadmin password file is empty... Aborting job"
    exit 1
  fi

  timeout --foreground 5m bash <<-EOF
    while ! oc login "${openshift_api}" -u "kubeadmin" -p "${openshift_password}" --insecure-skip-tls-verify=true; do
      sleep 20
    done
EOF
}

ci_configure_git_credentials() {
  git config --global user.name "redhat-appstudio-qe-bot"
  git config --global user.email redhat-appstudio-qe-bot@redhat.com
  mkdir -p "${HOME}/creds"
  local git_creds_path="${HOME}/creds/file"
  git config --global credential.helper "store --file ${git_creds_path}"
  echo "https://${GITHUB_USER}:${GITHUB_TOKEN}@github.com" > "${git_creds_path}"
}
