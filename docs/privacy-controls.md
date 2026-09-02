# 隐私承诺如何被测试钉住

栖境的隐私说明写的是“不得”和“只允许”。下表把这些句子映射到实现和自动化测试。没有测试的行，就还只是设计目标。

| 隐私承诺 | 实现位置 | 测试 | 现状 |
| --- | --- | --- | --- |
| 路径、文件名不得进入 Agent payload | `internal/privacy.Isolate`：Agent 视图没有 Path / Relative / Name | `TestAgentDataNeverContainsPathsOrNames` | 有测试 |
| 云端 payload 使用旋转匿名 ID，不含真实路径 | `internal/agent` payload 构造 | `TestCloudPayloadPrivacyAndRotatingIDs` | 有测试 |
| Agent 必须先确认，且确认令牌一次性 | `internal/application` agent 服务 | `TestAgentRequiresNetworkAndExactOneTimeConfirmation` | 有测试 |
| API Key 不进 SQLite | Windows DPAPI；设置里的 key 不写库 | `TestAPIKeyIsNotPersistedInSQLite`；`TestOriginRejectedAndModelKeyNeverReturned` | 有测试 |
| 扫描只在显式白名单根内 | `internal/scanner` 拒绝空授权 | `TestScanRequiresExplicitAllowlist`；`TestAuthorizeRootsIsAtomicAndReportsInvalidPaths` | 有测试 |
| 不得越过白名单根 | `internal/pathsafe.Contained` / `ValidateRoot` | `TestContainedRejectsEscape`；`TestValidateRootRejectsSymlink`；`TestJunctionEscapeRejected`；`TestRecyclePathValidationRejectsUnsafeTargets`；`TestRecyclePathValidationRejectsSymlinkedComponents` | 有测试 |
| 默认不读取文件正文 | 扫描默认 metadata-only；整盘哈希必须显式 opt-in | `TestWholeDriveHashRequiresExplicitOptIn`；`TestScanInvariantsRelationsAndExclusions` | 有测试 |
| 默认不联网 | Agent 入口要求打开联网并确认 | `TestAgentRequiresNetworkAndExactOneTimeConfirmation` | 有测试 |
| 清理只进入回收站，不永久删除 | 主进程回收；扫描器进程无写能力 | `TestPrivacyReportsRecycleCapabilityHonestly`；`TestRecycleCandidatesOnlyOffersDisposableClasses`；`TestRecycleConfirmationIsSingleUseAndBound` | 有测试 |
| 回收确认绑定预览快照，文件变了就拒绝 | 预览指纹含大小与修改时间 | `TestRecycleRefusesFilesChangedSincePreview`；`TestRecycleConfirmationExpiresAndIsSnapshotBound` | 有测试 |
| 扫描取消必须停在安全的部分结果 | 扫描预算 / cancel 返回 partial | `TestScanCancellationReturnsSafePartialResult`；`TestCancelledPartialRestoresOnRestart` | 有测试 |
| 服务只听 loopback | HTTP 预览拒绝非本机地址 | `TestServerRejectsNonLoopbackAddress` | 有测试 |
| 索引不进入可能漫游的配置目录；迁移不丢数据 | `internal/appdir`：LocalAppData 布局、完整性检查、备份、staging、原子切换、标记 | `TestNormalMigrationMovesDataAndKeepsLegacy`；`TestBackupFailureLeavesNoPartialState`；`TestStagingFailureRemovesStaging` | 有测试 |
| 数据目录 DACL 限制到当前用户 | `internal/appdir`：受保护 DACL（用户/SYSTEM/Administrators） | `TestRestrictToCurrentUserMakesDirPrivate`；`TestVerifyUserPrivateRejectsEveryoneGrant`；`TestChildInheritsRestriction` | 有测试 |
| API Key 密文不放在漫游目录 | DPAPI 密文写入 `%LocalAppData%\Qijing\data\secrets` | 密文迁移为纯复制，测试见 `TestSecretsMigrationCopiesOnlyBlobs` | 有测试 |

还没有单独自动化、但代码路径存在的项：云端占位文件跳过、junction 环、超长路径、磁盘拔出、SQLite 被占用。这些会放进 Windows CI，而不是继续只写在文档里。
