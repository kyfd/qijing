# ADR 0003：Windows 稳定文件身份与整理前身份重验

状态：已接受（2026-09-03）

## Context（背景）

「整理与回收」是栖境唯一会触碰用户文件的路径。此前的两阶段确认把
路径 + 大小 + 修改时间作为"用户看到的那一个文件"的凭据：预览时记录，
执行前重新 Lstat 比对。

## Problem（问题）

1. 路径不是文件身份：重命名、同卷移动都会让同一路径指向不同对象。
2. 大小和修改时间不是防替换凭据：一个恶意或粗心的程序可以在预览与
   确认之间删除原文件、在原路径创建一个大小和 mtime 完全一致的新文件，
   用户确认的却是他从未见过的内容。
3. 快照与文件的关系无法跨重命名保持，后续增量扫描（v1.x）无从谈起。

## Requirements（要求）

- 记录 Windows 稳定身份：Volume Serial Number + File Reference Number，
  连同观察时的大小、修改时间、创建时间。
- 整理执行前必须重新打开目标并比对完整身份；任何字段不一致即拒绝。
- 拿不到稳定身份时不得伪称身份成立（fail closed）。
- 打开方式必须只读元数据且不干扰其他程序（零访问权 + 全共享模式 +
  BACKUP_SEMANTICS），并支持长路径（`\\?\` 前缀）。
- 身份逻辑集中在一个包里，供回收流程和后续快照存储复用。

## Options（备选方案）

1. **仅路径 + size + mtime（现状）**：实现不了防替换，被否决。
2. **打开句柄并保持到确认**：跨用户交互持有句柄会阻止文件被替换
   （也阻止正常使用），Windows 语义上还可能阻止删除，副作用过大，被否决。
3. **NTFS 硬链接匹配**：需要为每个候选创建链接，本身是对用户目录的写，
   违反零写边界，被否决。
4. **`(volume_serial, file_id)` + 元数据快照（采用）**：纯元数据读取，
   零写边界不受影响，是 Windows 上标准的文件对象身份。

## Decision（决策）

采用方案 4，实现位于 `internal/fileid`：

- `Identify(path)`：零访问权打开 + `FILE_FLAG_BACKUP_SEMANTICS` +
  全共享模式，读取 `BY_HANDLE_FILE_INFORMATION` 得到卷序列号、
  64 位文件引用号、大小、修改时间、创建时间；
- `Matches(other)`：全部字段一致才算同一文件；FileID 与卷序列号均为零
  （文件系统不支持稳定 ID，如 FAT/exFAT）时一律不匹配，回退路径下
  系统只会拒绝更多操作而不会放行；
- 整理流程接入：`PreviewRecycle` 捕获身份并参与 selectionHash，
  `recycleOne` 在执行 `SHFileOperationW(FOF_ALLOWUNDO)` 前重新
  `Identify` 并比对，不一致返回 `ErrRecycleChanged`。

## Security implications（安全影响）

- 防替换强度从"统计信息一致"提升到"文件对象一致 + 统计信息一致"。
  典型攻击（删除后在原路径重建同大小同 mtime 的文件）会被 NTFS/ReFS
  的引用号序列增量识破。
- 剩余风险如实记录：FAT/exFAT 没有 FileID（返回 0），身份检查退化为
  size+mtime 比对（与旧行为等同，没有更弱）；NTFS 在极端情况下可能
  复用引用号（序列号增量使其概率极低）；持有同一用户权限的恶意软件
  本来就在威胁模型之外。不使用"绝对安全"表述。
- 识别过程零写入、零网络；打开的是用户已授权且逐项确认过的路径。

## Failure modes（失败模式）

| 失败 | 行为 |
| --- | --- |
| 执行前文件已删除 | Identify 报错，拒绝执行 |
| 文件被替换（同 stat） | FileID 不一致，拒绝执行 |
| 文件内容被修改（同路径同对象） | 大小/mtime 不一致，拒绝执行 |
| 时间戳精度差异导致误拒 | 时间戳取自同一句柄的同一结构，两次识别使用同源精度；不会因显示格式产生误判 |
| FAT/exFAT 无 FileID | Matches fail-closed：不匹配即拒绝（宁可误拒） |

## Migration（迁移路径）

无数据迁移。身份只在预览→确认的内存生命周期内存在，应用重启后确认
本就作废（既有语义）。后续 P0-3 存储重设计将把身份持久化进快照
（`snapshot_entries` / `file_identities`），本包是其唯一实现来源。

## Testing strategy（测试策略）

- `internal/fileid`：同一文件两次识别一致；不同文件不匹配；删除重建
  （同大小同 mtime）后身份必须不同（文件系统复用引用号时如实 skip）；
  目录可用；相对路径拒绝；零身份永不匹配。
- `internal/application`：TOCTOU 替换测试——预览后删除并用相同的
  大小与 mtime 重建，`recycleOne` 必须以 `ErrRecycleChanged` 拒绝，
  文件保持原样。
- 既有回收测试（一次性令牌、过期、快照绑定、变更拒绝）全部保持通过。

## Rollback strategy（回滚策略）

身份校验集中在 `recycleOne` 的一个比较步骤；回退只需移除该步骤并
恢复 Lstat 比较，不涉及 schema。
