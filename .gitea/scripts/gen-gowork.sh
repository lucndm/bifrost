#!/usr/bin/env bash
# Sinh go.work cho CI (go.work bị .gitignore nên không có trong checkout).
#
# - use: core, framework, transports, cli + mọi plugins/* có go.mod
# - replace kèm version, đọc trực tiếp từ transports/go.mod, cho mọi module
#   github.com/maximhq/bifrost/* có thư mục local: build không cần tải module
#   chưa publish (guardrails/quotatracker/tokensaver) và dùng đúng bản
#   fork-patched của otel. Version lấy động nên tự bám khi upstream bump.
set -euo pipefail

gover=$(sed -n 's/^go \([0-9.]*\)$/\1/p' core/go.mod | head -1)
mods="core framework transports cli"
for d in plugins/*/; do
  [ -f "${d}go.mod" ] && mods="$mods ${d%/}"
done

{
  echo "go $gover"
  echo
  echo "use ("
  for m in $mods; do printf '\t./%s\n' "$m"; done
  echo ")"
  echo
  sed -n 's/^\tgithub\.com\/maximhq\/bifrost\/\([^ ]*\) \(v[^ ]*\)$/\1 \2/p' transports/go.mod |
    while read -r rel ver; do
      if [ -f "./${rel}/go.mod" ]; then
        printf 'replace github.com/maximhq/bifrost/%s %s => ./%s\n' "$rel" "$ver" "$rel"
      fi
    done
} > go.work
