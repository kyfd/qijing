# 栖境

Windows 上的只读文件观察工具。给你授权过的目录画一张生态地图：谁占空间、哪里在涨、哪些像闲置或重复。它只看、只说明，不替你删、挪、改文件。

点托盘图标打开窗口。默认不联网、不听端口。需要的话可以接 OpenAI 兼容接口做只读巡视，发出去的是匿名汇总，发送前会让你确认。

## 运行

需要 Windows 10/11 和 [WebView2](https://developer.microsoft.com/microsoft-edge/webview2/)。

```powershell
.\build\windows\ecosystem-desktop.exe
```

- 启动后只待在托盘，主窗口默认不弹
- 「设置 → 观察目录」里选文件夹；也可以确认后再扫本机固定磁盘
- 关窗口是藏到托盘，托盘菜单里的「退出」才真正关掉
- 不会扫 U 盘、光驱、网络盘

## 构建

```powershell
git clone https://github.com/kyfd/qijing.git
cd qijing
go test -tags production ./...
go build -tags production -trimpath `
  -ldflags="-H windowsgui -s -w" `
  -o build\windows\ecosystem-desktop.exe `
  .\cmd\ecosystem-desktop
```

要 Go 1.26.3+。换图标时先改 `assets/appicon.png` 和 `internal/appicon/appicon.ico`，再跑 `go run .\cmd\icon-resource`。

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

## 不会做什么

不自动清理，不判断「可以删」，也不保证两个文件内容相同（没开本地哈希时只看元数据）。分类都是启发式，仅供参考。

授权目录里的文件它动不了。应用自己的配置、索引和审计写在私有数据目录里。细节见 [隐私说明](docs/privacy.md) 和 [架构说明](docs/architecture.md)。

## 文档

- [架构](docs/architecture.md)
- [隐私](docs/privacy.md)
- [开发](docs/development.md)
