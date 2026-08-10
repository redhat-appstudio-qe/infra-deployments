#!/bin/bash -e

main() {
    create_namespace openshift-pipelines
    echo "Setting secrets for pipeline-service"
    create_namespace tekton-logging
    create_namespace product-kubearchive-logging

    local minio_user minio_pass
    read -r minio_user minio_pass < <(get_or_create_minio_credentials)

    create_s3_secret openshift-pipelines tekton-results-s3 "$minio_user" "$minio_pass"
    create_s3_secret tekton-logging tekton-results-s3 "$minio_user" "$minio_pass"
}

create_namespace() {
    if kubectl get namespace $1 &>/dev/null; then
        echo "$1 namespace already exists, skipping creation"
        return
    fi
    kubectl create namespace $1 -o yaml --dry-run=client | kubectl apply -f-
}

get_or_create_minio_credentials() {
    if kubectl get secret -n openshift-pipelines minio-storage-configuration &>/dev/null; then
        echo "MinIO config already exists, reusing existing credentials" >&2
        local config
        config="$(kubectl get secret -n openshift-pipelines minio-storage-configuration -o jsonpath='{.data.config\.env}' | base64 -d)"
        local user pass
        user="$(echo "$config" | sed -n 's/^export MINIO_ROOT_USER="\(.*\)"$/\1/p')"
        pass="$(echo "$config" | sed -n 's/^export MINIO_ROOT_PASSWORD="\(.*\)"$/\1/p')"
        echo "$user $pass"
        return
    fi

    echo "Creating MinIO config" >&2
    local user=minio
    local pass
    pass="$(openssl rand -base64 20)"
    cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: minio-storage-configuration
  namespace: openshift-pipelines
type: Opaque
stringData:
  config.env: |-
    export MINIO_ROOT_USER="$user"
    export MINIO_ROOT_PASSWORD="$pass"
    export MINIO_STORAGE_CLASS_STANDARD="EC:1"
    export MINIO_BROWSER="on"
EOF
    echo "$user $pass"
}

create_s3_secret() {
    local namespace=$1
    local secret_name=$2
    local user=$3
    local pass=$4

    echo "Creating S3 secret in $namespace" >&2
    if kubectl get secret -n "$namespace" "$secret_name" &>/dev/null; then
        echo "S3 secret already exists, skipping creation"
        return
    fi
    kubectl create secret generic -n "$namespace" "$secret_name" \
      --from-literal=aws_access_key_id="$user" \
      --from-literal=aws_secret_access_key="$pass" \
      --from-literal=aws_region='not-applicable' \
      --from-literal=bucket=tekton-results \
      --from-literal=endpoint='https://minio.openshift-pipelines.svc.cluster.local' \
      --from-literal=s3_url='s3://tekton-results'
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
