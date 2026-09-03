# Changelog

## Unreleased

### Trust Hardening（v0.1.x）

- **按需读取路径**：扫描结束后不再把整份快照的条目留在内存里，
  `ScanManager` 只保留快照元数据，地图 / 节点详情 / 整理候选 / 统计 /
  建议全部改为对 SQLite 的有界查询。新增归一化的 `entry_classes` 表
  （migration 8，含既有快照回填），类别筛选从 JSON 扫描变为索引查找；
  分页排序以 id 打破大小并列，不重复不漏行。隐私视图与 Agent payload
  改为逐行流式聚合：payload 仍然只从允许字段构造，不存在"先复制整条
  记录再裁剪"的中间对象。地图读取失败时渲染为空并保留真实统计，
  而不是给出会低估快照规模的部分结果。
  （[ADR 0005](docs/adr/0005-on-demand-read-path.md)）
- **扫描可暂停 / 继续**：协议新增 `pause`/`resume` 帧，扫描进程在暂停闸门处
  停止产出条目但保持心跳存活；主进程 `ScanManager.Pause()/Resume()` 与
  `POST /api/v1/scan/pause|resume` 打通到界面按钮，暂停状态随扫描进度返回。
  暂停中的扫描仍可取消，取消后 staging 快照按既有规则降级为 incomplete。
- **扫描子进程 CPU 计量**：Job Object 记账（`JOBOBJECT_BASIC_ACCOUNTING_INFORMATION`）
  在作业句柄关闭前采样子进程用户态 + 内核态 CPU 时间，供基准测试如实记录；
  取不到时为 0，不做估算。
- **基准环境采集**（`internal/sysinfo`）：Windows 版本、CPU 型号与逻辑核心数、
  物理内存、目标卷的总线类型与驱动器类型，让性能数字带上可复现的环境上下文。
- **扫描迁入独立进程**：桌面主进程通过 Scanner Broker（`internal/scanbroker`）
  拉起 `qijing-scanner.exe`，以版本化的 Named Pipe IPC（`internal/scanproto` +
  `internal/ipcpipe`）通信：管道 DACL 只允许当前用户连接、8 MiB 帧上限、
  心跳超时、取消与整体超时。主进程对每条返回 entry 重新校验授权根与
  RootID，越权即终止扫描。扫描进程带 Job Object（kill-on-close），
  主进程退出即被回收；其依赖图不含数据库/Agent/回收站/设置代码，
  并由 `go list -deps` 守护测试钉住。缺少扫描器可执行文件时显式报错，
  不静默回退进程内扫描。
  （[ADR 0002](docs/adr/0002-scanner-subprocess-ipc.md)）
- **扫描结果流式分批落库（staging 快照）**：主进程不再"全量驻留内存后
  单事务写库"。Broker 把校验过的 entry 批次按小事务写入
  `%LocalAppData%\Qijing\data\ecosystem.db`，快照经历
  `staging → complete/partial/cancelled` 状态机；崩溃、取消、磁盘不足
  都会把 staging 快照降级为 `incomplete` 并清空其数据，永不作为结果
  展示，上一完整快照始终保留。启动时自动清理残留 staging 快照；
  数据卷可用空间低于 512 MiB 时主动停止扫描（fail closed）。
  新增 `scan_jobs` 表与索引（migration 7），N-1 升级有测试。
  （[ADR 0004](docs/adr/0004-streaming-scan-storage.md)）
- **整理动作改用 Windows 稳定文件身份防替换**：预览捕获
  (卷序列号, 文件引用号, 大小, 创建/修改时间)（`internal/fileid`）并纳入
  选择指纹；执行前重新打开文件比对完整身份，被替换成同大小同时间戳的
  同名文件也会被拒绝（`ErrRecycleChanged`）。拿不到稳定身份的文件系统
  上一律 fail-closed。识别过程零写入、零网络。
  （[ADR 0003](docs/adr/0003-windows-file-identity.md)）
- **命令行入口对齐目标结构**：`cmd/qijing`（正式桌面应用）、
  `cmd/qijing-scanner`（独立扫描进程）、`cmd/qijing-preview`（仅开发调试，
  桌面版不引用）。正式桌面构建仍然不监听任何 TCP 端口。
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
