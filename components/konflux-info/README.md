# 🚀 konflux-info Repository Guide

## 📂 Directory Structure

The `KONFLUX-INFO` directory contains:

```bash
.
├── auto-alert-schema.json  # JSON shema definition for auto-alert-content.yaml
├── base/                   # Common resources (e.g., RBAC)
├── production/             # Production cluster configurations
├── staging/                # Staging cluster configurations
├── banner-schema.json      # JSON schema definition for validating banner-content.yaml files

```

Each cluster directory contains:

```bash
.
├── auto-alerts # The directory manages auto-generated alerts content shown in the UI
├── banner-content.yaml # The banner content shown in the UI
├── info.json # Metadata about the cluster
└── kustomization.yaml # Kustomize configuration for this cluster, including base, auto-alerts, and other configs

```

---

## ✅ Banner Content Validation

A GitHub workflow named `banner-validate` automatically checks that each `banner-content.yaml` file conforms to the schema defined in `banner-schema.json`.  
This workflow runs whenever either the schema or any `banner-content.yaml` file is changed.  
The schema (`banner-schema.json`) specifies the required structure and fields for banner content, ensuring consistency and correctness across environments.

---

## 📝 How to submit a PR for Banner

1. Modify only the files relevant to your target cluster, e.g.: `staging/stone-stage-p01/banner-content.yaml` or `production/kflux-ocp-p01/banner-content.yaml`
2. In your PR description, include:

- Target cluster (e.g. kflux-ocp-p01)
- Type of change (e.g. new banner / update info / typo fix)
- Purpose of change (e.g. downgrade notification / release announcement)

---

## 📢 Auto Alerts

We enables the infrastructure team to automatically surface specific operational issues or warnings in the Konflux UI.

These alerts would be auto-generated from monitoring systems or automation scripts, written as Kubernetes ConfigMaps, and automatically picked up by the Konflux UI to inform users of system-wide conditions.

### ✅ Alert YAML Format

Each file under auto-alerts/ must be a valid Kubernetes ConfigMap, including at minimum:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: konflux-auto-alert-xyz
  namespace: konflux-info
  labels:
    konflux-auto-alert: "true" # Required. UI filter alerts out by this label.
data:
  auto-alert-content.yaml: |
    summary: "Builds are delayed due to maintenance"
    type: "warning"
```

🔐 The data.banner-content.yaml should follow the schema defined in `auto-alert-schema.json`

### Folder Structure

```bash

auto-alerts/   # Alert ConfigMaps (one file = one alert)
.
├── alert-1.yaml           # Fully valid ConfigMap YAML
├── alert-2.yaml
└── kustomization.yaml     # Auto-generated, includes all alert YAMLs

```
