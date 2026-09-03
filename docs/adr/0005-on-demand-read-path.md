# ADR 0005：按需读取路径与 entry_classes 归一化表

状态：已接受（2026-09-03）

## Context（背景）

ADR 0004 把写入路径改成了流式：entry 分批经 IPC 到达，分批小事务写入
SQLite。但读取路径没有跟上——`ScanManager` 仍然把整份快照的 entry 保留
在内存里（`model.Scan.Entries`），地图、节点详情、整理候选、统计数字和
Agent payload 全部基于这份内存副本。

## Problem（问题）

1. 内存占用与文件数量线性相关：一次 100 万文件的扫描结束后，进程会长期
   持有这 100 万条 entry，直到下一次扫描替换它。这与"有界资源使用"原则
   直接冲突，也让 `MaxEntries` 预算变成了内存上限而不是安全上限。
2. 所有筛选都是内存全表扫描：`classes` 以 JSON 数组存在 `entries` 表里，
   任何"按类别筛选"的查询要么在 Go 里遍历全部 entry，要么在 SQL 里做
   JSON 扫描，两者都随快照大小线性劣化。
3. 隐私路径与 Agent payload 先构造完整的内存集合再裁剪，形状上接近
   目标文档明令禁止的"构造完整对象 → 删除敏感字段"。

## Decision（决策）

**1. entry 只存在于 SQLite。** `ScanManager.run()` 在保存成功后把
`result.Entries` 与 `result.Relations` 置空，只保留快照元数据（id、
起止时间、状态、根目录、错误计数）。因此任意大小的快照，常驻内存成本
相同。`store.Scan` 与 `store.ScanMeta` 拆分开：需要元数据的调用者不再
被迫拉取全部 entry。

**2. 新增 `entry_classes` 归一化表（migration 8）。**

```sql
CREATE TABLE entry_classes(
  scan_id TEXT NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
  entry_id TEXT NOT NULL, class TEXT NOT NULL,
  PRIMARY KEY(scan_id,entry_id,class));
CREATE INDEX entry_classes_scan_class ON entry_classes(scan_id,class);
CREATE INDEX entries_scan_kind_size ON entries(scan_id,kind,size DESC);
```

类别谓词因此是索引查找而不是 JSON 扫描。`entries.classes` 保留为
真相来源，`entry_classes` 是它的索引投影，两者在同一事务里写入。

**3. 有界查询取代内存遍历。** `internal/store/entries.go`：

| 查询 | 用途 | 边界 |
| --- | --- | --- |
| `EntryStats` | 统计数字 | 聚合，不返回行 |
| `Entry` / `EntriesByID` | 节点详情 / 回收站预览 | 按 id |
| `LargestEntries` | 地图 | `LIMIT` + 诚实的总数 |
| `FlaggedEntries` | 建议列表 | `LIMIT` |
| `EntriesWithClasses` | 整理候选 | `LIMIT`/`OFFSET` 分页 |
| `EachEntry` | 隐私视图 / Agent payload | 逐行回调，不物化 |

排序一律为 `ORDER BY size DESC, id`（或 `ORDER BY id`）：用 id 打破
大小相等时的并列，分页才不会重复或漏行。

**4. 全量消费者改为流式。** `privacy.IsolateStream` 与
`agent.BuildCloudPayloadStream` 接受一个 `each` 回调游标。Agent payload
把 entry 折叠进 `regionAccumulator` 后立即丢弃，`RegionSummary` 逐字段
从允许字段构造——不存在"先复制再裁剪"的中间对象。

## Security implications（安全影响）

- Agent payload 的构造方式从"复制后裁剪"变为"只从允许字段构造"。
  `model.Entry` 新增字段不会默认泄漏，因为没有任何一处复制整个结构体。
- 隐私审计视图不再需要把整份快照读进内存才能展示，降低了敏感数据在
  进程内的驻留时间与规模。
- 读取失败时地图渲染为空而不是渲染成部分结果：部分结果会让用户以为
  快照比实际更小，这是一种会误导删除决策的错误。

## Failure modes（失败模式）

| 失败 | 行为 |
| --- | --- |
| 读取查询失败（地图） | 返回空节点集 + 真实统计，不返回部分节点 |
| 读取查询失败（候选） | 停止分页，返回已获得的候选 |
| 快照被后续扫描替换 | 查询按 scan_id 限定，跨快照不会串数据 |
| 请求了不存在的 entry | `ErrEntryNotFound` / `ErrNodeNotFound`，不静默跳过 |

## Migration（迁移路径）

migration 8 建表建索引，并用 JSON1 的 `json_each` 一次性回填既有快照的
`entry_classes`。回填只在升级时执行一次，之后写入路径直接维护该表。
`TestMigrationBackfillsEntryClassesForExistingSnapshots` 在 N-1 schema 上
种入旧数据后跑完整迁移链，校验旧快照升级后仍可按类别查询、统计正确。

## Testing strategy（测试策略）

- Store：top-N 与诚实总数；大小相等时排序稳定；统计在空快照上为零而非
  报错；entry 查找受快照隔离；`EntriesByID` 只返回被请求的行；类别 +
  kind 过滤；分页不重叠不漏行；空输入不退化为无过滤读取；`EachEntry`
  流完所有行并传播回调错误；abandon 清空 `entry_classes`；N-1 迁移回填。
- Application：地图对整份快照诚实报告截断（`NodesTotal` /
  `NodesOmitted`），统计仍描述全部文件；回收站候选只提供可处置类别；
  回收站预览按 id 从库中读取，未知 id 仍返回 `ErrNodeNotFound`。

## Rollback strategy（回滚策略）

回退 = 恢复 `ScanManager.run()` 里对 `result.Entries` 的保留，并让各
读取路径改回内存遍历。`entry_classes` 表与索引可以保留，不影响旧行为；
schema 只增不改。
