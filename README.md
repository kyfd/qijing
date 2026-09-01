# 栖境

Windows 上的文件观察工具。给你授权过的目录画一张生态地图：谁占空间、哪里在涨、哪些像闲置或重复。观察全程只读；它只看、只说明，除非你逐项确认，否则不碰你的文件。

点托盘图标打开窗口。默认不联网、不听端口。需要的话可以接 OpenAI 兼容接口做只读巡视，发出去的是匿名汇总，发送前会让你确认。

## 普通用户

需要 Windows 10/11 和 [WebView2](https://developer.microsoft.com/microsoft-edge/webview2/)。

当前还没有代码签名证书，也还没有 GitHub Release 安装包。Windows 可能会弹出 SmartScreen，这是未签名程序的正常提示，不是产品已经“发布完成”。请从下面的源码构建，或等 [Releases](https://github.com/kyfd/qijing/releases) 出现 `qijing-windows-amd64.zip` 后再下载。

构建出来的桌面程序：

```powershell
.\build\windows\ecosystem-desktop.exe
```

- 启动后只待在托盘，主窗口默认不弹
- 「设置 → 观察目录」里选文件夹；也可以确认后再扫本机固定磁盘
- 关窗口是藏到托盘，托盘菜单里的「退出」才真正关掉
- 不会扫 U 盘、光驱、网络盘

请依次看：白名单授权、扫描进度、分类 / 空间分布、发给模型前的匿名 payload、移入回收站前的逐项确认。这些是产品边界，不是装饰。

## 开发者：从源码构建

不要一上来就为了“点一下看看”去装完整开发环境。只有改代码时才需要 Go 和 WebView2 开发组件。

```powershell
git clone https://github.com/kyfd/qijing.git
cd qijing
go test -tags production ./...
go build -tags production -trimpath `
  -ldflags="-H windowsgui -s -w" `
  -o build\windows\ecosystem-desktop.exe `
  .\cmd\ecosystem-desktop
```

`go.mod` 声明的是 **Go 1.26.3**，因为这是当前核实过的构建工具链，不是因为代码依赖某个 1.26 补丁特性。更低版本尚未做兼容认证。换图标时先改 `assets/appicon.png` 和 `internal/appicon/appicon.ico`，再跑 `go run .\cmd\icon-resource`。

Go module 路径是 `github.com/kyfd/qijing`。

独立扫描器（只往 stdout 打匿名结果）：

```powershell
go build -o dist\ecosystem-scanner.exe .\cmd\ecosystem-scanner
```

## 模型（可选）

「设置 → Agent 模型」里填 OpenAI 兼容的 Base URL 和模型名。云端要 API Key，Ollama / LM Studio 可以留空。Key 用当前用户的 DPAPI 加密，不进 SQLite。

打开联网后点「让 Agent 巡视」，先核对 payload 再跑。payload 里没有真实路径、文件名或文件内容。

## 开发预览

`cmd/ecosystem` 是带本地端口的预览，桌面版不用它：

```text
ecosystem serve      127.0.0.1:8765
ecosystem status
ecosystem open
ecosystem privacy
```

## 整理与回收

「整理」面板里会列出符合临时、缓存或残留特征的文件。勾选之后还要再确认一次，最终只做一件事：把文件移进 Windows 回收站。它不会永久删除、不会清空回收站、不会移动或重命名文件。每次回收都写进本机的回收记录，即使快照被清掉也留着。

只有被判为 rotten（长期未访问的临时/缓存类）或 orphan（残留）的文件才会出现在候选里。单纯的大文件和休眠文件永远不进候选。

## 不会做什么

不自动清理，不判断「可以删」，也不保证两个文件内容相同（没开本地哈希时只看元数据）。分类都是启发式，仅供参考。

扫描器进程本身没有任何写文件的能力。回收只由主进程在你确认后执行，且只能移进回收站。应用自己的配置、索引和审计写在私有数据目录里。细节见 [隐私说明](docs/privacy.md)、[隐私控制对照](docs/privacy-controls.md) 和 [架构说明](docs/architecture.md)。

## 文档

- [架构](docs/architecture.md)
- [隐私](docs/privacy.md)
- [隐私控制对照](docs/privacy-controls.md)
- [开发](docs/development.md)
