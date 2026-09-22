#!/usr/bin/env bash
# Gate của bifrost — chạy bởi workflow verify/sync dùng chung
# (minhluc-info/ci). Gồm: build toàn bộ module Go, test plugin fork +
# transports, build UI, kiểm tra pin Dockerfile.fork <-> transports/go.mod.
set -euo pipefail
cd "$(dirname "$0")/.."

# UI build chạy nền cho nhanh; không dùng `npm run build` vì copy-build của nó
# sẽ ghi đè transports/bifrost-http/ui trong lúc Go đang build (đua file).
( cd ui && npm ci && npx vite build && npx tsc --noEmit ) &
UI_PID=$!

# go.work bị gitignore -> sinh lại; placeholder cho go:embed all:ui
bash ./.gitea/scripts/gen-gowork.sh
mkdir -p transports/bifrost-http/ui
[ -n "$(ls -A transports/bifrost-http/ui)" ] || echo placeholder > transports/bifrost-http/ui/.gitkeep

for m in $(go list -m -f '{{if .Main}}{{.Dir}}{{end}}' all | grep .); do
  echo "== build $m"
  go -C "$m" build ./...
done

for p in adaptive guardrails quotatracker tokensaver; do
  echo "== test plugins/$p"
  go -C "plugins/$p" test ./...
done

echo "== test transports"
go -C transports test ./...

echo "== pin check Dockerfile.fork <-> transports/go.mod"
fail=0
for mod in otel; do
  want=$(sed -n "s/^[[:space:]]*github.com\/maximhq\/bifrost\/plugins\/${mod} \(v[^ ]*\)$/\1/p" transports/go.mod | head -1)
  if [ -z "$want" ]; then
    echo "không tìm thấy plugins/${mod} trong transports/go.mod"
    fail=1
    continue
  fi
  grep -q "plugins/${mod} ${want} => ./plugins/${mod}" transports/Dockerfile.fork || {
    echo "MISMATCH go.work replace: plugins/${mod} (go.mod=${want})"
    fail=1
  }
  grep -q "plugins/${mod}@${want}=" transports/Dockerfile.fork || {
    echo "MISMATCH mod edit -replace: plugins/${mod} (go.mod=${want})"
    fail=1
  }
done
[ "$fail" = "0" ] || exit 1

wait "$UI_PID"
echo "== gate PASS"
