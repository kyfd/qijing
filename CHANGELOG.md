# Changelog

## 0.1.0 - 2026-09-02

First public Windows build.

- Local-first desktop observer with an empty allowlist
- Metadata-only scan by default; local hashing is opt-in
- Recycle Bin only after per-item confirmation
- Agent payloads stay anonymous; real paths and names are excluded
- Go module path is `github.com/kyfd/qijing`

The executable is **not code-signed**. Windows SmartScreen may warn. That is expected for an unsigned build, not a completed store release.
