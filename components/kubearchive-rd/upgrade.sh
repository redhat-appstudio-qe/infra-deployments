#!/bin/bash -e

# Usage: upgrade.sh <old-version> <new-version> [<new-migration-version>]
# Example: upgrade.sh v1.21.3 v1.21.4
# Example: upgrade.sh v1.21.4 v1.22.0 14
#
# Upgrades the ring-0 (development) KubeArchive deployment.
#
# The upstream release manifest is split into one file per resource under
# kubearchive-operator/ (see directory-layout.md). This script re-fetches
# the new release, splits it the same way, and overwrites each existing
# file by matching on (kind, name) so the per-resource layout is preserved.

OLD_VERSION=$1
NEW_VERSION=$2
NEW_MIGRATION_VERSION=$3

OPERATOR_DIR=components/kubearchive-rd/rings/ring-0/base/kubearchive-operator
RING_KUSTOMIZATION=components/kubearchive-rd/rings/ring-0/base/kustomization.yaml
PATCHES_DIR=components/kubearchive-rd/rings/ring-0/base/patches

echo "Upgrading KubeArchive (ring-0/development) from $OLD_VERSION to $NEW_VERSION..."

TMP_MANIFEST=$(mktemp)
curl -Lo "$TMP_MANIFEST" "https://github.com/kubearchive/kubearchive/releases/download/$NEW_VERSION/kubearchive.yaml"

# Build a map of "kind/name" -> existing file path from what's on disk today.
declare -A EXISTING_FILES
while IFS= read -r -d '' f; do
  k=$(yq eval '.kind' "$f")
  n=$(yq eval '.metadata.name' "$f")
  [ "$k" = "null" ] || [ "$n" = "null" ] && continue
  EXISTING_FILES["$k/$n"]="$f"
done < <(find "$OPERATOR_DIR" -name '*.yaml' ! -name 'kustomization.yaml' -print0)

# Split the new manifest by resource and overwrite matching existing files.
TMP_SPLIT_DIR=$(mktemp -d)
csplit -s -z -f "$TMP_SPLIT_DIR/doc-" "$TMP_MANIFEST" '/^---$/' '{*}'

for doc in "$TMP_SPLIT_DIR"/doc-*; do
  sed -i '1{/^---$/d}' "$doc"
  kind=$(yq eval '.kind' "$doc")
  name=$(yq eval '.metadata.name' "$doc")
  [ "$kind" = "null" ] || [ "$name" = "null" ] && continue

  dest="${EXISTING_FILES[$kind/$name]}"
  if [ -z "$dest" ]; then
    echo "WARNING: new upstream resource $kind/$name has no existing file - add it to $OPERATOR_DIR manually" >&2
    continue
  fi
  cp "$doc" "$dest"
  unset "EXISTING_FILES[$kind/$name]"
done

for remaining in "${EXISTING_FILES[@]}"; do
  echo "WARNING: $remaining was not present in the new release - remove it manually if upstream dropped it" >&2
done

rm -rf "$TMP_SPLIT_DIR" "$TMP_MANIFEST"

# Vacuum CronJob images are pinned to the KubeArchive release train via
# ring-authored patches (the vacuum jobs are defined in this repo, not
# upstream, so they aren't covered by the manifest re-split above).
sed -i "s/vacuum:$OLD_VERSION/vacuum:$NEW_VERSION/g" "$PATCHES_DIR"/*.yaml

if [ -n "$NEW_MIGRATION_VERSION" ]; then
  OLD_MIGRATION_VERSION=$(grep -m1 'MIGRATION_VERSION=' "$RING_KUSTOMIZATION" | sed 's/.*MIGRATION_VERSION=//')

  echo "Updating schema migration from v$OLD_MIGRATION_VERSION to v$NEW_MIGRATION_VERSION..."

  sed -i "s/MIGRATION_VERSION=$OLD_MIGRATION_VERSION/MIGRATION_VERSION=$NEW_MIGRATION_VERSION/g" "$RING_KUSTOMIZATION"
  sed -i "s/kubearchive-schema-migration-v$OLD_MIGRATION_VERSION-v$OLD_VERSION/kubearchive-schema-migration-v$NEW_MIGRATION_VERSION-v$NEW_VERSION/g" "$PATCHES_DIR/schema-migration-job-name-patch.yaml"
fi
