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

To maintain consistency, a GitHub workflow named **`banner-validate`** automatically validates all `banner-content.yaml` files against the schema defined in [`banner-schema.json`](./banner-schema.json).

**When does it run?**

- On any pull request that changes:
  - `banner-schema.json` (schema definition)
  - Any `banner-content.yaml` file (banner configurations)

**What does it check?**

- Ensures the YAML structure matches the schema (e.g., required fields, allowed values, date/time format).
- Prevents invalid or misconfigured banners from being merged.

**How to fix validation errors?**

- Review the error message in the PR checks.
- Compare your changes with the [schema](./banner-schema.json) and [examples in README](#usage-scenarios--examples).

## ✅ Banner Content Specification

The `banner-content.yaml` file defines one or more banners displayed in the Konflux UI. Each cluster has its own `banner-content.yaml` under its directory (e.g., `staging/stone-stage-p01/banner-content.yaml`).

### **Schema**

The schema for banner content is defined in [`banner-schema.json`](./banner-schema.json) and validated automatically by the `banner-validate` GitHub workflow on every PR.

The file must contain a **YAML list** where each item represents a banner configuration.

---

### **Important Behavior**

- The <strong style="color: red;">UI displays only the first valid active banner</strong> from the list, based on current date, time, and optional recurrence settings.
- If multiple banners are configured, <strong style="color: red;">order matters</strong>.

---

### **Required and Optional Fields for Each Banner**

📎 For the full schema used in CI validation, see banner-schema.json. This table is a human-friendly reference for banner authors.

| Field        | Type   | Required | Description                                                               |
| ------------ | ------ | -------- | ------------------------------------------------------------------------- |
| `summary`    | string | ✅       | Banner text (5–500 chars). **Supports Markdown** (e.g., bold, links).     |
| `type`       | string | ✅       | Banner type: `info`, `warning`, or `danger`.                              |
| `startTime`  | string | ⚠️\*     | Start time in `HH:mm` (24-hour). Required if date-related fields are set. |
| `endTime`    | string | ⚠️\*     | End time in `HH:mm` (24-hour). Required if date-related fields are set.   |
| `timeZone`   | string | ❌       | Optional IANA time zone (e.g., Asia/Shanghai).Omit for UTC (default).     |
| `year`       | number | ❌       | Year (1970–9999) for one-time banners.                                    |
| `month`      | number | ❌       | Month (1–12).                                                             |
| `dayOfWeek`  | number | ❌       | Day of week (0=Sunday, 6=Saturday) for weekly recurrence.                 |
| `dayOfMonth` | number | ❌       | Day of month (1–31). Required if `year` or `month` is specified.          |

⚠️ **If any of `year`, `month`, `dayOfWeek`, or `dayOfMonth` is specified, both `startTime` and `endTime` are required.**

---

### **Usage Scenarios & Examples**

#### ✅ **1. Multiple Banners**

Example of a `banner-content.yaml` with multiple banners (first active one is shown in UI):

```yaml
- summary: "Scheduled downtime on July 25"
  type: "warning"
  year: 2025
  month: 7
  dayOfMonth: 25
  startTime: "10:00"
  endTime: "14:00"
  timeZone: "America/Los_Angeles"

- summary: "Maintenance every Sunday"
  type: "info"
  dayOfWeek: 0
  startTime: "02:00"
  endTime: "04:00"
  # No timezone is needed when you expect it's UTC.
```

#### ✅ **2. One-Time Banner**

For a single event on a specific date:

```yaml
- summary: "Scheduled downtime on July 25"
  type: "warning"
  year: 2025
  month: 7
  dayOfMonth: 25
  startTime: "10:00"
  endTime: "14:00"
```

For a single event in today

```yaml
- summary: "Scheduled downtime on July 25"
  type: "warning"
  startTime: "10:00"
  endTime: "14:00"
```

#### ✅ **2. Weekly Recurring Banner**

For an event that repeats every week:

```yaml
- summary: "Maintenance every Sunday"
  type: "info"
  dayOfWeek: 0
  startTime: "02:00"
  endTime: "04:00"
```

#### ✅ **3. Monthly Recurring Banner**

For an event that happens on the same day each month:

```yaml
- summary: "Patch release on 1st of every month"
  type: "info"
  dayOfMonth: 1
  startTime: "01:00"
  endTime: "03:00"
  timeZone: "Asia/Shanghai"
```

#### ✅ **4. Always-On Banner**

For an event that requires immediate notification:

```yaml
- summary: "New feature: Pipeline Insights is live!"
  type: "info"
```

#### ✅ **5. Empty Banner**

When there are no events to announce:

```
[]
```

---

## 📝 How to submit a PR for Banner

1. Locate the target cluster directory:

- For staging: `staging/<cluster-name>/banner-content.yaml`
- For production: `production/<cluster-name>/banner-content.yaml`

2. Edit banner-content.yaml:

- <strong style="color: red;">Insert the new banner at the top of the list</strong>.
- Remove obsolete banners to keep the list clean.

  Example:

  ```yaml
  # New banner on top
  - summary: "New feature rollout on July 30"
    type: "info"
    year: 2025
    month: 7
    dayOfMonth: 30
    startTime: "09:00"
    endTime: "17:00"

  # Keep other active banners below
  - summary: "Maintenance every Sunday"
    type: "info"
    dayOfWeek: 0
    startTime: "02:00"
    endTime: "04:00"
  ```

3. Submit a Pull Request:

- Modify only the target cluster’s banner-content.yaml.
  In the PR description, include:
- Target cluster (e.g., kflux-ocp-p01)
- Type of change (e.g., new banner / update / remove obsolete)
- Purpose of change (e.g., release announcement, downtime notice)

  Example:

  ```yaml
  Target cluster: kflux-ocp-p01
  Type: New banner
  Purpose: Release announcement for Konflux 1.2
  ```

## ❓ Frequently Asked Questions

- Why is only one banner shown even when multiple are configured?

  <strong style="color: red;">We follow the [PatternFly design guidelines](https://www.patternfly.org/components/banner/design-guidelines) for banners</strong>, which emphasize simplicity and clarity. Showing just one banner line at a time helps avoid overwhelming users and ensures that important messages aren't lost in clutter.

- What does “first active” actually mean?

  <strong style="color: red;">The term 'first' doesn’t imply priority or severity</strong> — it simply refers to the first banner that is currently active based on time and repeat configuration.

  If a banner was scheduled in the past, it should already have been displayed.

  If it's scheduled in the future, it will show when its time comes.

  At any given moment, the system checks which banner is active right now, and picks the first one that matches the criteria.

  🕒 Banners use fields like `startTime`, `endTime`, `dayOfWeek`, etc., to precisely define when they should appear.

  <strong style="color: red;">📝 If multiple messages need to be shared at the same time, consider combining them into a well-written summary inside a single banner.</strong>

## 📢 Auto Alerts(WIP)

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
    enable: true
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
