# token-saver changelog

## 0.1.0 (2026-09-11)

- Phase 1 (ADR-0081): RTK passive compression in PreRequestHook.
  - Filters: git-diff (hunk cap), dedup-log (consecutive collapse + line cap),
    smart-truncate (head+tail).
  - Shapes: chat tool messages and responses function_call_output items.
  - Fail-open contract: per-blob guards, per-message recover, never touches
    is_error outputs or provider-native raw payloads.
  - Stats log line per request when savings > 0.

## 0.2.0 (2026-09-11)

- Settings overrides: `models.<glob>` (longest pattern wins, `path.Match`) and
  `virtual_keys.<name>` merged field-wise over `default` (precedence: model → VK → default).
- `Settings` fields are now pointers (`rtk`/`caveman`/`ponytail`) so overrides only
  replace fields they declare.
- PreRequestHook resolves settings from the post-routing model and the
  governance-resolved virtual-key name.
- Built-in startup registration fixed: token-saver now loads from config.json on
  server start (slot 6, after routing), not only via plugin CRUD API.
