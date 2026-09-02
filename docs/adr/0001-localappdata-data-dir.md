# ADR 0001：本地数据目录迁移到 %LocalAppData%\Qijing

状态：已接受（2026-09-02）

## Context（背景）

栖境当前把 SQLite 索引、扫描历史和审计写在 `os.UserConfigDir()`，即
`%AppData%\FileEcosystem`。这是 **漫游（Roaming）** 配置目录：域环境或
Windows 备份/漫游配置文件可能把它复制到其他机器。扫描索引包含真实路径、
文件名和目录结构，属于“不应离开设备”的本地敏感数据（见 docs/privacy.md）。

API Key 的 DPAPI 密文同样放在 `%AppData%\github.com\kyfd\qijing\secrets`。
DPAPI 密文与当前用户和机器绑定，漫游到其他机器也无法解密，只会扩大无意义的
泄露面。

## Problem（问题）

1. 敏感本地索引可能被漫游机制复制到用户不可见的其他位置。
2. 目录名 `FileEcosystem` 是历史遗留，与产品名不一致。
3. 没有统一规划 data / logs / cache / backups / updates 的目录布局。
4. 已有用户的旧数据需要无损迁移，不能因为升级而丢失扫描历史和审计记录。

## Requirements（要求）

- 新数据根固定为 `%LocalAppData%\Qijing`，子目录：`data`、`logs`、`cache`、
  `backups`、`updates`；DPAPI 密文放 `data\secrets`。
- 迁移必须按序完成：检测旧目录 → 数据库完整性检查 → 迁移前备份 →
  临时目录复制 → 迁移后再次检查 → 原子切换 → 失败恢复 → 写完成标记。
- 任何一步失败：保留旧目录原样，清理临时目录，向调用方报错，绝不出现
  “一半在新一半在旧”的状态。
- 迁移永远不删除旧目录（自动删除用户数据超出授权；旧目录即备份）。
- 目录权限继承用户配置文件 ACL（仅当前用户、SYSTEM、Administrators），
  并有测试验证不引入 Everyone/Users 等宽泛授权。
- 迁移逻辑必须可以在测试中用临时目录完整演练，包括失败路径。

## Options（备选方案）

1. **维持 %AppData%**：零工作量，但违反隐私边界，被否决。
2. **%ProgramData%**：机器级共享目录，权限模型更宽，被否决。
3. **%LocalAppData%\Qijing + 保守复制迁移（采用）**：与 Windows 生态惯例
   一致（浏览器、VS Code 均将本地数据放 %LocalAppData%），迁移是纯复制，
   旧目录始终保留为备份。

## Decision（决策）

采用方案 3。实现位于 `internal/appdir`：

- `Ensure()` 解析布局、建目录、执行一次性迁移；
- 数据迁移：完整性检查（`PRAGMA quick_check`）→ 整目录复制到
  `backups\pre-migration-<时间戳>`（迁移前备份）→ 复制到 staging →
  再次完整性检查 → `os.Rename` 原子切换为 `data` → 写
  `migration.complete` 标记；
- 新位置已有数据时以新位置为准（升级重装场景），只补写标记；
- Secret 迁移：将旧 namespace 目录下的 `*.dpapi` 文件复制到
  `data\secrets`；DPAPI 密文同一 Windows 用户下解密结果不变。

## Security implications（安全影响）

- 索引不再进入漫游配置文件，减少一条敏感数据离开设备的路径。
- 新目录通过继承 ACL 限制到当前用户；`verifyUserPrivate` 在 NTFS 上校验
  DACL 不包含 Everyone/Users 授权，非 NTFS（无 ACL 语义）跳过并记录原因。
- 迁移过程不打开、不修改、不删除任何用户授权目录中的文件；只复制应用
  自己的私有数据。
- DPAPI 密文按文件原子写入（tmp + rename），迁移为复制语义，不重加密。

## Failure modes（失败模式）

| 失败 | 行为 |
| --- | --- |
| 旧库 quick_check 失败 | 放弃迁移，报错；旧目录原样，用户数据无损 |
| 复制中断/磁盘满 | staging 清理，报错；旧目录原样 |
| 备份校验失败 | 放弃迁移，报错 |
| 切换前 data 已存在 | 保留新位置数据（不覆盖），补写标记 |
| 标记写入失败 | 下次启动重新执行迁移；因复制是幂等的，结果一致 |

## Migration（迁移路径）

桌面应用与开发预览启动时调用 `appdir.Ensure()`。首次在新版本启动即完成
迁移，无用户交互。回滚到旧版本时应用继续读旧目录（新目录对旧版本不可见，
不存在共享损坏面）。

## Testing strategy（测试策略）

`internal/appdir` 单元测试覆盖：全新安装（无旧目录）、正常迁移、旧库损坏、
备份损坏、staging 校验失败、目标已存在、标记已存在、Secret 复制、
Unicode/空格路径。Windows 专属测试验证目录 ACL。所有测试使用 `t.TempDir()`，
不触碰真实用户目录。

## Rollback strategy（回滚策略）

迁移不删除、不改写旧目录；回退旧版本即自动回到旧目录。新目录可以通过
用户手动删除（“删除全部本地数据”路径后续任务提供界面入口）。
