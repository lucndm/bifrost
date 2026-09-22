---
name: sync-upstream
description: Sync nhánh fork với upstream qua Gitea Actions (mirror/bifrost, minhluc-info/bugsink, mirror/tolaria dùng shared repo minhluc-info/ci). Debug CI fail (đọc log MinIO/DB), xử lý conflict sync, release/deploy bifrost, thêm repo mới vào pipeline dùng chung. Invoked with /sync-upstream [repo] hoặc /sync-upstream debug <run-id>.
allowed-tools: Bash, Read, Grep, Glob, Edit, Write, Task, AskUserQuestion
---

# Sync Upstream — fork homelab (bifrost / bugsink / tolaria)

## Kiến trúc

- Logic dùng chung: repo **`minhluc-info/ci`** (public) — `.gitea/workflows/{sync-upstream,verify}.yml` là reusable workflows.
- Mỗi repo chỉ có: trigger file (`.gitea/workflows/sync-upstream.yml` + `verify.yml`) và gate `.ci/verify.sh`.
- Nguồn upstream: nhánh `upstream-<branch>` NGAY TRONG repo (bot cập nhật, đã strip `.github/workflows/*`).
- Docs đầy đủ: README của `minhluc-info/ci`.

## Repo đang dùng

| Repo | upstream | target | gate |
|---|---|---|---|
| `mirror/bifrost` | `upstream-dev` | `dev` | `.ci/verify.sh`: build 20 module Go + test plugin fork + transports + UI build + pin check otel |
| `minhluc-info/bugsink` | `upstream-main` | `main` | `.ci/verify.sh`: ruff + wheel + makemigrations + test (postgres pigsty, DB tên riêng theo run + drop) |
| `mirror/tolaria` | `upstream-main` | `main` | `.ci/verify.sh`: pnpm install → tsc → vite build → vitest (bỏ releaseDownloadPage.test.ts) → eslint |

## Sync tay (khi workflow fail / cần can thiệp)

```bash
git fetch origin && git switch <target_branch>
git merge origin/<upstream_branch>
# resolve conflict → chạy gate: bash .ci/verify.sh → push
```

- Conflict hay gặp: `.github/workflows/*` modify/delete (fork move sang `.gitea/workflows/`) — giữ bản fork.
- Go: conflict ở `transports/go.mod` → lấy version upstream cho module chung, GIỮ module fork (guardrails/quotatracker/tokensaver).
- Sau merge bắt buộc: bump pin `plugins/otel` trong `transports/Dockerfile.fork` khớp `transports/go.mod` (bifrost).

## Debug CI fail

1. Task/step status: DB gitea `postgres://192.168.100.59:5439` — bảng `action_task`, `action_task_step` (status: 1=success 2=failure 4=skipped 6=running).
2. Log: MinIO `192.168.100.59:8333` bucket `gitea`, key = `actions_log/` + `action_task.log_filename` (`.log.zst`, giải nén `zstd -dc`).
3. Lỗi `docker.sock: no such file` = **DinD chết giữa job** — hạ tầng, chạy lại; KHÔNG phải lỗi code.
4. Log rỗng + job chết giữa step → cũng là DinD. Check runner pods: `kubectl -n gitea-runner get pods`.

## Quirks Gitea Actions 1.27 (đã verify)

- Expressions chỉ hỗ trợ `always()`; `if: failure()` không chạy → dùng `always()` + check `steps.*.outcome` trong script.
- `on: schedule` không hỗ trợ cho scoped workflow; reusable workflow cross-repo cần repo nguồn đọc được (org chứa nó phải public) — `minhluc-info` đã public.
- Gitea đọc CẢ `.github/workflows/` và `.gitea/workflows/` — fork CI đặt `.gitea/workflows/` vì bot strip `.github/workflows/*` trên nhánh upstream.
- Background process trong step giữ log pipe → **job treo dù step success**. Không chạy nền.
- Runner image (`docker.gitea.com/runner-images:ubuntu-latest`): có Node 24 + corepack, KHÔNG có Go/Python → dùng inputs `setup_go`/`setup_python` của shared workflow.

## Quy tắc

- Workflow chỉ push nhánh đích (`dev`/`main`). **Không bao giờ push `upstream-*`** (đó là mirror bot).
- Secrets k8s do Infisical quản (`InfisicalStaticSecret`, refresh 1h) — KHÔNG patch k8s secret trực tiếp; thêm key trong Infisical. Nhiều session/agent có thể đang sửa cùng hạ tầng — hỏi trước khi ghi vào secret/deployment.
- Thêm repo mới vào pipeline: xem README của `minhluc-info/ci` (trigger file + `.ci/verify.sh` + đổi default branch).
