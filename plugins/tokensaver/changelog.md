# token-saver changelog

## 0.1.0 (2026-09-11)

- Phase 1 (ADR-0081): RTK passive compression in PreRequestHook.
  - Filters: git-diff (hunk cap), dedup-log (consecutive collapse + line cap),
    smart-truncate (head+tail).
  - Shapes: chat tool messages and responses function_call_output items.
  - Fail-open contract: per-blob guards, per-message recover, never touches
    is_error outputs or provider-native raw payloads.
  - Stats log line per request when savings > 0.
