# Trust Manager

This component deploys [trust-manager](https://cert-manager.io/docs/projects/trust-manager/) using Helm.

## Overview

Trust Manager is a Kubernetes controller that distributes trust bundles (X.509 certificate authority certificates) across namespaces and automatically updates them when they change. It's part of the cert-manager ecosystem and helps with certificate management in Kubernetes clusters.

## Deployment

The component is deployed using:
- **Helm Chart**: `jetstack/trust-manager`
- **Version**: `0.9.0`
- **Namespace**: `cert-manager`
- **Method**: HelmChartInflationGenerator (Kustomize)

## Configuration

The component is currently configured with default values from the Helm chart. Custom values can be added by creating a `trust-manager-helm-values.yaml` file and referencing it in the generator.

## Dependencies

- Requires cert-manager to be installed in the cluster
- Uses the jetstack Helm repository

## Environments

Currently deployed in:
- Development environment only

