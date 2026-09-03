# ADR 0004：流式扫描存储与 staging 快照

状态：已接受（2026-09-03）

## Context（背景）

P0-1 完成后，扫描在独立进程中进行，entry 以分批形式经 IPC 到达主进程，
但持久化仍是"全量驻留内存 → 单事务写库"：`ScanManager` 在扫描结束后
调用 `store.SaveScan`，把几十万条 entry 一次性写入 SQLite。

## Problem（问题）

1. 单事务全量写入：数据库写入慢时内存无界增长，且写入中途断电会让
   数据库长时间处于写事务中。
2. 没有 staging 语义：崩溃、取消、断电后无法区分"写完的快照"和
   "写了一半的快照"。
3. 没有低磁盘空间保护：索引可能把系统盘写满。

## Requirements（要求）

- Scanner →（IPC 分批）→ Broker →（分批事务）→ SQLite，每一级都有界；
  数据库写不过来时产生 backpressure（阻塞 IPC，而不是堆积内存）。
- 中途取消或崩溃产生 INCOMPLETE/STAGING 快照；未完成快照不得作为
  完整结果展示；新快照完成前保留上一完整快照。
- 应用重启后能检测并清理残留 staging 快照。
- 低磁盘空间时主动停止扫描。
- schema_migrations 保持版本化，N-1 升级有测试。

## Options（备选方案）

1. **维持单事务全量写**：被否决（上述问题）。
2. **内存队列 + 后台写线程**：引入异步错误处理与清空语义，复杂度高；
   由于 IPC 本身是流式的，同步写即天然背压，被否决。
3. **同步分批事务 + staging 状态机（采用）**：Broker 的会话循环直接把
   校验过的批次写入库（每批一个事务）；慢盘 → 批写慢 → IPC 阻塞 →
   扫描器减速。整条管线每一级的缓冲都有界（批 512 条、管道 1 MiB）。

## Decision（决策）

采用方案 3。数据模型沿用 `scans` / `entries` / `relations` /
`scan_errors`（即目标模型中的 snapshots / snapshot_entries 等），新增：

- 快照状态机：`staging` → `complete` / `partial` / `cancelled` /
  `incomplete`。`staging` 与 `incomplete` 永不出现在结果查询中
  （`LatestScan` 过滤）。
- `scan_jobs` 表（migration 7）：记录每次 Broker 运行与快照的对应关系，
  重启清理有据可查；`scans(status)`、`entries(scan_id)` 建索引。
- Store API：`BeginStagingScan` / `WriteEntryBatch` / `FinalizeScan` /
  `AbandonScan` / `PurgeStagingScans`。
- Broker 侧 `Sink` 接口：`BeginStaging` →（每批）`WriteEntries` →
  `Finalize`；任何失败走 `Abandon`（删除已流入的 entry，保留行作诊断）。
  快照 ID 由 Broker 生成并通过 ScanRequest 下发，扫描器只是沿用。
- 低磁盘保护：sink 在打开快照时和每 10 批检查一次数据卷可用空间
  （`internal/diskspace`），低于 512 MiB 即以 `low_disk` 代码放弃快照并
  终止扫描；磁盘不可答时同样 fail closed。
- 启动清理：`application.New` 调用 `PurgeStagingScans`，把所有残留
  staging 快照降级为 incomplete 并清空其 entry；上一完整快照不受影响。
- `ScanManager` 对实现了 `PersistsOwnResults()` 的引擎跳过旧的全量
  `SaveScan`，避免双写。

## Security implications（安全影响）

- 数据卷写满不再可能由扫描造成：保护阈值触发时停止写入、清理未完成
  数据，用户目录始终零写入。
- staging / incomplete 快照不进入任何结果视图，越权或中断数据不会
  因为"读到一半"而出现。
- 分批事务缩小了崩溃窗口：任意时刻数据库里的 entry 都属于一个可识别
  的 staging 快照，重启后被整体清理，不存在"无法归属"的数据。

## Failure modes（失败模式）

| 失败 | 行为 |
| --- | --- |
| 写库中途崩溃 | staging 行残留，重启时清理；上一完整快照完好 |
| 用户取消 | staging 快照标记 incomplete，entry 清空 |
| 低磁盘空间 | 扫描停止，报 `low_disk`；上一完整快照完好 |
| Finalize 失败 | 整个快照走 Abandon，不会出现"半完成"的 complete |
| 扫描器崩溃 | 同 P0-1；加上 staging 清理，数据库无孤儿 entry |

## Migration（迁移路径）

migration 7 追加 `scan_jobs` 表与索引；旧数据不需要变换。N-1 升级测试
（`TestMigrationFromPreviousSchemaPreservesScans`）在上一版 schema 上
种入数据后执行完整迁移链并校验可读。

## Testing strategy（测试策略）

- Store：staging 生命周期（staging 不可见 → 分批写入 → finalize 可见 →
  不可二次 finalize）；abandon 隐藏并清空；启动清理保留上一完整快照；
  N-1 迁移保留旧数据。
- Broker：真实服务循环 + 记录型 sink，校验 begin → batches → finalize
  顺序；sink 失败以 `low_disk` 代码 abandon；扫描器崩溃 abandon。
- Application：`PersistsOwnResults` 引擎不触发二次保存（以冲突行探测）；
  重启清理计数回调。

## 已知边界与后续切片

读取路径已在 ADR 0005 中切换为按需查询 SQLite，本 ADR 遗留的"内存副本"
边界不再存在。剩余切片是前端虚拟化与分层地图加载。

## Rollback strategy（回滚策略）

回退 = 恢复旧的 `SaveScan` 调用；`scan_jobs` 表与状态过滤可以保留，
不影响旧行为。schema 只增不改。
