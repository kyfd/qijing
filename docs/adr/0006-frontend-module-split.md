# ADR 0006：前端模块拆分与布局 Web Worker

状态：已接受（2026-09-06）

## Context（背景）

`web/app.js` 最初是 1065 行的单 IIFE：传输、扫描状态、地图布局与渲染、
授权、Agent、回收、隐私审计全部共享一个作用域。修改任何一处都要理解
整个文件，也无法对纯函数（状态归一、布局）单独做测试。同时布局计算
（圆簇松弛，90 轮两两碰撞消解）在主线程运行，几百个节点重排时 UI 掉帧。

## Problem（问题）

1. 单文件模块不可维护，无法按功能演进（地图、扫描、Agent 各自加速）。
2. 布局在主线程阻塞交互。
3. 嵌入式资源没有缓存校验头：应用升级后 WebView2 可能继续用旧模块，
   用户看到的不是自己运行的版本的行为。

## Requirements（要求）

- 不引入构建步骤与第三方依赖（原则 7：简单可靠架构）；
- 行为与拆分前等价（同一份函数体，仅移动位置）；
- 布局计算可移出主线程，且在 Worker 不可用时回退主线程；
- 静态资源必须带 `Cache-Control: no-cache`，升级后不允许出现旧界面。

## Options（备选方案）

1. **保持单文件**：不可维护，被否决。
2. **引入打包器（Vite/esbuild）**：能力最强，但引入 Node 工具链与构建
   产物一致性负担，违背"零依赖本地界面"，被否决。
3. **原生 ES Modules + Web Worker（采用）**：WebView2 是常青 Chromium，
   module 与 module worker 原生支持；`http.FileServer` 直接服务子目录；
   桌面端 Wails asset server 把请求全部交给应用自身 handler，路径一致。

## Decision（决策）

采用方案 3。目录与职责：

```text
web/app.js            入口：只做事件绑定与 bootstrap
web/api/adapter.js    传输层：桌面原生桥接 / 本地 HTTP，同一组方法
web/state/state.js    全局状态、分区定义、演示数据
web/components/dom.js $、escapeHtml、toast、格式化、Markdown 渲染
web/map/              节点管道：layoutCore（纯函数）/ layout（Worker 编排）
                      / nodes / render / view / sidebar / detail / demo
web/scan/             status（纯解析）/ ui / controller（生命周期）/ roots
web/agent/            profile（模型配置）/ run（巡视流程）
web/recycle/          整理候选 → 预览 → 确认 → 回收站
web/settings/         设置页签
web/diagnostics/      隐私与审计视图
web/workers/          layout.worker.js：布局计算
```

- 布局走 Worker：主线程把 `{id, nodes, aspect}` 交给 Worker，Worker 返回
  坐标，主线程按位拷回**原对象**——结构化克隆意味着 Worker 改的是副本，
  直接丢弃返回值会让节点永远停在 (0,0)（首次浏览器验收时抓到的真实缺陷）。
  按位拷回保住了对象身份，搜索与聚合展开的 `includes()` 依赖它。
- Worker 构造失败或脚本错误时回退主线程同源实现（`layoutCore.js` 同时
  被两端 import），结果一致。
- `internal/server` 对非 API 路径统一加 `Cache-Control: no-cache`；
  桌面端复用同一 handler，因此同样受保护。

## Security implications（安全影响）

- 无新增网络访问：Worker 与所有模块同源加载自本地资源。
- `escapeHtml` 与 Markdown 渲染集中到 `components/dom.js`，Agent 报告
  仍先转义再标记，不产生新的注入面。

## Failure modes（失败模式）

| 失败 | 行为 |
| --- | --- |
| Worker 构造失败（策略/环境） | 回退主线程同步布局，界面照常 |
| Worker 脚本加载失败 | `onerror` 一次性降级并唤醒所有等待者，不悬挂 |
| 布局消息返回前用户拖拽 | 旧坐标短暂可见，坐标到位后重绘 |
| 应用升级后旧模块缓存 | `no-cache` 强制重校验，运行新版本 |

## Migration（迁移路径）

已完成一次性拆分；后续前端工作直接在对应模块内进行。模块间依赖保持
单向（入口 → 功能模块 → api/state/components），scan/roots 与
scan/controller 之间的相互引用只允许发生在函数体内（ES 模块活绑定）。

## Testing strategy（测试策略）

- `node --input-type=module --check` 对每个模块做语法门禁；
- 预览服务器 + 浏览器验收：真实快照（43 万条目）下地图渲染、节点点击、
  搜索、设置/整理/隐私对话框逐项走通；
- 服务器测试钉住 no-cache 行为；嵌入模式由 `go:embed` 编译期校验。

## Rollback strategy（回滚策略）

单提交整体回退即可恢复单文件版本；无数据与协议影响。
