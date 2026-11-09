#!/bin/sh
# Keeps the newest tags of a repository in the DigitalOcean registry and deletes the rest, then
# runs the registry's garbage collection (deleting a tag frees nothing until it runs). Every
# release of an installation is a tag, so without this the registry fills up: the Starter tier
# holds 500 MiB, about ten releases of a 50 MiB image.
#
#   registry-prune.sh <repository> [keep]      keep defaults to 10
#
# Needs doctl, authenticated (DIGITALOCEAN_ACCESS_TOKEN). DRY_RUN=1 only lists what it would
# delete. Tags named in KEEP_TAGS (space separated) are never deleted: put the release that is
# running in it, so a pruned rollback target never surprises you.
set -eu

repo="${1:?usage: registry-prune.sh <repository> [keep]}"
keep="${2:-10}"
case "$keep" in
  '' | *[!0-9]*) echo "registry-prune: keep must be a number, got '$keep'" >&2; exit 2 ;;
esac
[ "$keep" -ge 1 ] || { echo "registry-prune: keep must be at least 1" >&2; exit 2; }

# Newest first.
tags=$(doctl registry repository list-tags "$repo" --format Tag,UpdatedAt --no-header | sort -k2 -r | awk '{print $1}')

n=0
deleted=0
for tag in $tags; do
  n=$((n + 1))
  [ "$n" -gt "$keep" ] || continue
  case " ${KEEP_TAGS:-} " in *" $tag "*) echo "keep  $repo:$tag (KEEP_TAGS)"; continue ;; esac
  if [ "${DRY_RUN:-}" = 1 ]; then
    echo "would delete  $repo:$tag"
  else
    doctl registry repository delete-tag "$repo" "$tag" --force
    echo "deleted  $repo:$tag"
  fi
  deleted=$((deleted + 1))
done

if [ "${DRY_RUN:-}" = 1 ]; then
  echo "dry run: $deleted tag(s) would be deleted, $((n - deleted)) kept"
elif [ "$deleted" -gt 0 ]; then
  doctl registry garbage-collection start --include-untagged-manifests --force
  echo "garbage collection started: $deleted tag(s) deleted, $((n - deleted)) kept"
else
  echo "nothing to prune: $n tag(s), keeping $keep"
fi
