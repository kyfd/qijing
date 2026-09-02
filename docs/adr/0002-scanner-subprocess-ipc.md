# ADR 0002：独立扫描子进程与 Named Pipe IPC

状态：已接受（2026-09-02）

## Context（背景）

当前桌面主进程在进程内直接运行 `internal/scanner`（`application.New` 的
factory 直接 `scanner.New(cfg)`）。这意味着扫描代码与数据库、Agent、回收
站、设置等能力共享一个地址空间：任何一处的内存错误都可能波及其余部分，
也无法用进程边界证明"扫描器没有写能力"。

`cmd/qijing-scanner` 目前只是一个独立的 stdout 演示工具，不参与桌面扫描。

## Problem（问题）

1. 无法用进程隔离支撑隐私文档中"扫描器进程保持零写能力"的承诺。
2. 主进程退出、崩溃或被强杀时，没有机制保证扫描任务随之结束。
3. 缺少心跳、取消、超时与消息大小上限等商用级 IPC 语义。
4. 主进程对扫描结果没有第二道校验：如果扫描代码有 bug 或被篡改，
   越权路径会直接进入索引。

## Requirements（要求）

- 扫描在独立进程 `qijing-scanner.exe` 中执行；桌面主进程通过 Scanner
  Broker 拉起并与之通信。
- scanner 进程不 import 数据库、Agent、回收站、设置等业务包（用
  `go list -deps` 守护测试钉住）。
- 主进程退出（含被强杀）时 scanner 必须退出：Job Object +
  `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`，并在正常路径显式 Terminate。
- IPC 通道权限限制到当前用户；协议显式版本化；握手失败立即终止。
- 支持心跳、取消、整体超时、单条消息大小上限。
- 主进程对每一条返回的 entry 重新验证其路径位于本次授权根内；验证失败
  视为致命协议违规，终止扫描并保留既有完整快照。
- scanner 崩溃不能破坏已有完整快照：崩溃时丢弃未保存的进行中结果，
  已保存的快照保持不变。

## Options（备选方案）

1. **保持进程内扫描**：最简单，但隔离承诺永远只是文档，被否决。
2. **COM / RPC**：Windows 原生但复杂度高、调试困难，被否决。
3. **gRPC over UDS**：引入大依赖与生成代码，违背简单架构原则，被否决。
4. **Named Pipe + 长度前缀 JSON 帧（采用）**：x/sys/windows 已在依赖中，
   实现小、可测、可用 SDDL 精确限权；协议层与传输层分离后可注入测试。

## Decision（决策）

采用方案 4。结构：

```text
internal/scanproto    协议：版本、帧定义、编解码、大小上限（纯 io 流，可移植）
internal/ipcpipe      传输：Windows Named Pipe（SDDL 限当前用户）/ 非 Windows 回退
internal/scannerproc  scanner 侧服务循环：握手、请求、批量上报、心跳、取消
internal/scanbroker   主进程侧 Broker：拉起进程、Job Object、心跳超时、
                      取消、结果重验、组装 model.Scan
cmd/qijing-scanner    独立扫描进程入口（只 import 扫描与协议层）
```

协议要点：

- 帧 = 4 字节大端长度 + JSON 体；超过 `MaxFrameBytes`（8 MiB）即协议违规。
- 握手：client 发 `hello{version}`，server 回 `hello_ack{version}`，
  版本不一致立即断开。
- server → client：`progress`、`entries`（分批 entry）、`relations`、
  `done`（最终状态与计数）、`heartbeat`、`fatal`（已脱敏的错误码）。
- client → server：`scan`（ScanRequest：授权根 + 显式 ScanOptions 白名单
  字段）、`cancel`。
- 心跳：scanner 空闲时每 10s 发一帧；Broker 超过 45s 无任何帧判定死亡。
- Job Object 由主进程创建并分配子进程，`KILL_ON_JOB_CLOSE` 保证主进程
  无论正常退出还是被强杀，子进程都会被回收。
- Broker 重新验证：发送前用 `pathsafe.ValidateRoot` 校验每个根；对每条
  entry 用 `pathsafe.Contained` 复核路径属于本次授权根，并核对 RootID；
  任一失败 = 致命违规，终止并报错（错误信息只含错误码，不含路径）。

## Security implications（安全影响）

- 进程边界使"scanner 无写能力、无回收站能力、不访问 Agent/数据库"成为
  可被 `go list -deps` 守护测试验证的事实。
- Named Pipe 的 DACL 只授予当前用户 `GENERIC_ALL`；其他用户与低完整性
  进程无法连接。管道名含随机后缀，不可预测。
- 同一用户的本地管道上会传输真实路径与文件名（扫描索引本身就需要）。
  这不违反隐私边界：数据不离开设备、不进入 Agent payload；日志与
  fatal 错误码保持脱敏。
- 主进程重验是纵深防御：即使 scanner 进程被替换或出现 bug，越权数据也
  不会进入索引。
- Job Object 与显式 Terminate 双保险，杜绝扫描器孤儿进程。

## Failure modes（失败模式）

| 失败 | 行为 |
| --- | --- |
| scanner exe 缺失 | 启动扫描即失败并给出明确错误（不静默回退进程内扫描） |
| 握手版本不匹配 | 立即终止，报协议版本错误 |
| 心跳超时 / 无响应 | Broker 终止子进程，扫描失败，已有快照不受影响 |
| scanner 崩溃 | 同上；未保存的进行中结果被丢弃 |
| 越权 entry | 致命违规：终止扫描、丢弃结果、记录脱敏错误码 |
| 用户取消 | 发送 cancel，宽限期后强制终止；保留 partial 状态语义 |
| 管道被占用/断开 | 重试连接短暂窗口后失败 |

## Migration（迁移路径）

`application` 的 scan factory 切换为 Broker 引擎；进程内引擎仅保留给
单元测试。桌面开发环境需要把 `qijing-scanner.exe` 放在主程序同目录
（或用 `QIJING_SCANNER_EXE` 指定）；发布包同时携带两个可执行文件。

## Testing strategy（测试策略）

- 协议层：编解码 roundtrip、大小上限、版本不匹配。
- 服务循环：内存连接上驱动假引擎，覆盖 happy path、取消、引擎错误、心跳。
- Broker：真实 Named Pipe + 进程内假 scanner：结果组装、越权重验拒绝、
  猝死/静默（超时）、取消、崩溃不落库。
- 集成（非 `-short`）：真实构建 `qijing-scanner.exe` 并对临时夹具树做
  端到端扫描。
- 守护测试：`go list -deps ./cmd/qijing-scanner` 不得包含
  store/agent/platform/llm/secret/desktop 包。

## Rollback strategy（回滚策略）

factory 是唯一接线点；回退为进程内引擎只需要恢复一个函数。协议与传输
包独立存在，不影响其他路径。
