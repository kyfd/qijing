# 栖境

Windows 文件观察工具，用来查看授权目录的空间占用、变化和闲置候选。扫描阶段不修改源文件；如需回收文件，必须在整理面板中逐项选择并确认。

点托盘图标打开窗口。默认不联网、不听端口。需要的话可以接 OpenAI 兼容接口做只读巡视，发出去的是匿名汇总，发送前会让你确认。

## 普通用户

需要 Windows 10/11 和 [WebView2](https://developer.microsoft.com/microsoft-edge/webview2/)。

从 [Releases](https://github.com/kyfd/qijing/releases) 下载 `qijing-windows-amd64.zip`，核对 `SHA256SUMS`，然后运行解压出的 `qijing.exe`。

当前发布包未做代码签名，Windows SmartScreen 可能提示“未识别的应用”。请核对下载来源和校验值；这类提示本身不能证明程序安全。

构建出来的桌面程序：

```powershell
.\build\windows\ecosystem-desktop.exe
```

- 启动后只待在托盘，主窗口默认不弹
- 「设置 → 观察目录」里选文件夹；也可以确认后再扫本机固定磁盘
- 关窗口是藏到托盘，托盘菜单里的「退出」才真正关掉
- 不会扫 U 盘、光驱、网络盘

使用顺序是授权目录、等待扫描、查看分类和空间分布。调用模型前可以检查待发送的汇总数据；回收文件另有确认步骤。

## 开发者：从源码构建

使用发布包不需要安装 Go。从源码构建时需要 Go 和 WebView2 开发组件。

```powershell
git clone https://github.com/kyfd/qijing.git
cd qijing
go test -tags production ./...
go build -tags production -trimpath `
  -ldflags="-H windowsgui -s -w" `
  -o build\windows\ecosystem-desktop.exe `
  .\cmd\ecosystem-desktop
```

`go.mod` 声明 Go 1.26.3，更低版本的兼容性尚未验证。换图标时先改 `assets/appicon.png` 和 `internal/appicon/appicon.ico`，再跑 `go run .\cmd\icon-resource`。

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

扫描器代码不执行文件处置，回收由主进程在确认后调用 Windows Shell 完成。这是应用中的职责划分，不代表扫描进程受到操作系统级只读隔离。应用自己的配置、索引和审计写在私有数据目录里。细节见 [隐私说明](docs/privacy.md)、[隐私控制对照](docs/privacy-controls.md) 和 [架构说明](docs/architecture.md)。

## 文档

- [架构](docs/architecture.md)
- [隐私](docs/privacy.md)
- [隐私控制对照](docs/privacy-controls.md)
- [开发](docs/development.md)
