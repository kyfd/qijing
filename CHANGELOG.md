# Changelog

## Unreleased

### Trust Hardening（v0.1.x）

- **本地数据迁移到 `%LocalAppData%\Qijing`**：扫描索引、历史与审计不再存放
  于可能随漫游配置文件复制的 `%AppData%`。数据目录 DACL 明确限制到当前
  用户、SYSTEM 与 Administrators（受保护 DACL）。旧目录迁移按“完整性检查
  → 迁移前备份 → 临时目录复制 → 再次检查 → 原子切换 → 完成标记”执行，
  任何失败都保持旧数据原样；迁移逻辑由 `internal/appdir` 测试覆盖
  （[ADR 0001](docs/adr/0001-localappdata-data-dir.md)）。
- **API Key 密文目录同步迁移**：DPAPI 密文从 `%AppData%` 下的 namespace
  目录复制到 `%LocalAppData%\Qijing\data\secrets`，旧文件保留作备份。

## 0.1.0 - 2026-09-02

First public Windows build.

- Local-first desktop observer with an empty allowlist
- Metadata-only scan by default; local hashing is opt-in
- Recycle Bin only after per-item confirmation
- Agent payloads stay anonymous; real paths and names are excluded
- Go module path is `github.com/kyfd/qijing`

The executable is **not code-signed**. Windows SmartScreen may warn. That is expected for an unsigned build, not a completed store release.
