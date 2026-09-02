# 开发指南

本文面向 Windows 上参与“文件生态系统 Agent”开发的贡献者。项目处于早期阶段，命令入口、测试覆盖和构建产物可能尚未齐备；以下内容同时定义目标开发约定，不代表所有目标组件已经实现。

## 环境要求

- Windows 10 或 Windows 11（64 位）
- Go 1.26.3 或满足 `go.mod` 声明的更高兼容版本
- Microsoft Edge WebView2 Runtime
- PowerShell 7 或 Windows PowerShell
- Git（可选，但建议用于版本控制）

检查环境：

```powershell
go version
go env GOOS GOARCH GOMOD
```

仓库当前由 `go.mod` 明确声明 Go 1.26.3。若后续需要降低版本，必须通过依赖兼容性和全量测试后再修改声明与文档。

## 获取代码

```powershell
git clone https://github.com/kyfd/qijing.git
Set-Location qijing
go mod download
```

如果代码已经在本地：

```powershell
Set-Location qijing
go mod download
```

不要以管理员身份进行日常开发或运行扫描测试。最小权限有助于发现错误的权限假设。

## 项目约束

涉及扫描和文件系统代码时，以下约束不可破坏：

1. 用户授权目录初始为空，扫描只在显式白名单根内进行。
2. 应用永不永久删除、移动、重命名或改写授权目录中的文件与目录。唯一允许的文件处置是「整理与回收」：用户逐项确认后，把文件移入 Windows 回收站。
3. 默认扫描只访问元数据。
4. 只有用户对授权根开启本地哈希时，scanner 才能读取文件字节；原始字节仅用于本地摘要计算。
5. 默认不联网；任何外部 Agent 请求都必须显式选择加入，并使用字段 allowlist 生成匿名 payload。
6. Agent 输出不能决定授权、路径边界或文件操作；Agent 没有回收工具，也不得获得任何文件处置能力。
7. 扫描器进程保持零写能力，回收只在主进程中执行。
8. 测试不得扫描开发者真实的桌面、下载、文档、源码集合或整个磁盘；应使用临时夹具。

不要添加永久删除、移动、重命名、覆盖、解压、Git 写操作或 shell 命令执行器。Explorer 定位仅负责显示目标。

扩展回收功能时必须保留全部既有约束：候选仅限 rotten / orphan 分类；单批上限 50；preview → confirm 两阶段，令牌一次性、限时、常量时间比较；选择指纹绑定文件大小与修改时间；预览与执行两次执行完整路径校验；始终带 `FOF_ALLOWUNDO` 并保留 `FOF_WANTNUKEWARNING`；每项操作写审计。不要为了「更方便」而绕过其中任何一条。

## 开发循环

### 格式化

```powershell
gofmt -w .
```

提交前查看格式变化，并确保没有无关生成文件进入工作区。如果只需检查，可在 PowerShell 中运行：

```powershell
$unformatted = gofmt -l .
if ($unformatted) {
    $unformatted
    throw "存在未格式化的 Go 文件"
}
```

### 编译

```powershell
go build ./...
```

仓库早期阶段可能没有 `cmd` 入口；此命令成功只说明现有包可编译，不代表预计的 CLI、Web 界面、托盘或扫描流程已经可用。

### 单元与集成测试

```powershell
go test ./...
go test -count=1 ./...
```

需要详细输出时：

```powershell
go test -v ./...
```

竞态检测器依赖受支持的 Windows/Go 工具链及 C 编译环境。环境满足时运行：

```powershell
go test -race ./...
```

如果当前 Windows 工具链不支持 `-race`，应在 CI 或受支持环境补跑，并记录该验证缺口，不能把跳过视为通过。

### 静态检查

```powershell
go vet ./...
```

如项目后续引入额外静态检查器，应固定版本并在贡献说明或 CI 配置中保持一致。

## Windows 构建

### 当前包检查

适用于任何仓库阶段：

```powershell
$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build ./...
```

PowerShell 环境变量会留在当前会话中。构建后可清除：

```powershell
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue
```

在 Windows 本机通常无需显式设置 `GOOS`。

### 桌面程序入口

当前入口包括：

```text
cmd/qijing/            Wails WebView2 + Windows 通知区域桌面应用（正式桌面构建）
cmd/qijing-preview/    仅开发调试的显式 HTTP 预览与 CLI，桌面版不引用
cmd/qijing-scanner/    独立匿名只读扫描进程
```

桌面生产构建必须包含 Wails 的 `production` tag，并使用 Windows GUI subsystem。扫描在独立进程 `qijing-scanner.exe` 中执行，开发运行桌面程序前先把它构建到 `qijing.exe` 同目录（或用 `QIJING_SCANNER_EXE` 指定路径），否则启动扫描会得到明确报错，而不是静默回退到进程内扫描。若修改了 `internal\appicon\appicon.ico`，先重新生成资源对象；否则直接使用仓库内已生成的 amd64 资源：

```powershell
New-Item -ItemType Directory -Force .\build\windows | Out-Null
go run .\cmd\icon-resource # 仅在替换图标后运行
go build -tags production -trimpath `
  -ldflags="-H windowsgui -s -w" `
  -o .\build\windows\qijing.exe `
  .\cmd\qijing
```

图标母版为 `assets\appicon.png`，多尺寸 ICO 由 `internal\appicon` 嵌入托盘，并作为 Windows resource ID 3 编入 EXE，使窗口、任务栏和通知区域使用同一品牌标识。

桌面程序正常运行时不监听 localhost/TCP 端口，页面资源和 API 通过 Wails 进程内 handler 分发。构建机和目标机器需具备 Microsoft Edge WebView2 Runtime。

### 构建产物

构建产物应写入仓库的构建输出目录（如 `dist`），而不是任何测试授权目录。发布前应记录：

- Go 版本与目标架构；
- 是否启用 CGO；
- 测试、竞态检测和 `go vet` 状态；
- 尚未验证的平台能力；
- 默认网络状态与只读不变性测试结果。

## 测试策略

### 临时文件系统夹具

使用 `t.TempDir()` 创建完全受测试控制的目录。夹具应覆盖：

- 普通文件、空文件、嵌套目录和大量小文件；
- 大小写差异、空格、Unicode、长路径和 Windows 保留名边界；
- 无权限项和扫描中消失的文件；
- symlink、junction、mount point 或其他 reparse point；
- 云端占位属性（在可控环境中）；
- ZIP 与同名解压目录候选；
- Git 仓库只读状态；
- 本地哈希关闭和开启两种路径。

涉及 reparse point 的测试若需要额外 Windows 权限，应明确跳过原因，并在具备开发者模式或相应权限的 CI 环境补跑。

### 只读不变性测试

这是发布前的关键验证。测试应在扫描前后比较：

- 完整目录树及名称；
- 文件内容哈希；
- 文件大小；
- 创建时间和修改时间（考虑 Windows 文件系统精度）；
- 文件属性和权限；
- 是否出现新文件、替代数据流或临时文件。

分别验证：

1. 仅元数据扫描；
2. 开启本地哈希；
3. 扫描取消与错误恢复；
4. symlink/junction 逃逸尝试；
5. 未授权路径输入；
6. Explorer 定位调用前后的目标状态。

扫描路径的只读保证不能仅凭代码中没有明显的 `Remove` 或 `Rename` 来判断；应以能力审计和不变性测试共同证明。回收路径必须单独测试：越权路径、符号链接与重解析点、根目录本身、非常规文件都应被拒绝；令牌过期、重放和跨快照使用都应失败；文件在预览后变化时执行必须中止。

### 隐私测试

至少覆盖：

- 初始白名单为空；
- 未授权路径不会被扫描；
- 默认敏感目录被排除；
- 本地哈希关闭时不打开文件内容；
- Agent payload 使用字段 allowlist；
- 真实路径、文件名、哈希、Git 信息和自由文本不会进入 payload；
- 网络默认关闭，未选择加入时没有外部请求；
- 删除索引只改变应用私有数据库，源夹具保持不变。

### 本地服务安全测试

目标服务实现后验证：

- 只绑定 `127.0.0.1`，不监听外部接口；
- Host、Origin、会话令牌和状态变更请求校验；
- CORS 默认拒绝非预期来源；
- API 不存在永久删除、移动、重命名或写文件端点；回收端点必须要求有效的一次性确认令牌；
- 模型响应和文件名中的指令性文本不能触发权限变化或文件操作。

## 手动验证

只使用临时演示目录：

```powershell
$demo = Join-Path $env:TEMP "qijing-demo"
New-Item -ItemType Directory -Force $demo | Out-Null
Set-Content -LiteralPath (Join-Path $demo "example.txt") -Value "demo"
Get-ChildItem -LiteralPath $demo -Force
```

`Set-Content` 仅用于开发者显式创建测试夹具，不属于应用能力。实际应用应只把 `$demo` 作为白名单根，并保持目标文件不变。测试完成后，由开发者手动清理夹具：

```powershell
Remove-Item -LiteralPath $demo -Recurse -Force
```

不要把真实用户目录作为手动测试目标。

## 推荐提交前检查

```powershell
Set-Location qijing

$unformatted = gofmt -l .
if ($unformatted) {
    $unformatted
    throw "gofmt 检查失败"
}

go test ./...
go vet ./...
go build ./...
```

若改动影响 scanner、路径边界、隐私字段或网络能力，还应运行对应安全、不变性和 payload 测试。没有测试覆盖时，应把功能标记为目标或未验证，不能在文档和发布说明中宣称已完成。

## Windows 注意事项

- Windows 路径比较通常不区分大小写，但不能用简单字符串前缀判断根包含关系。
- 必须处理驱动器路径、UNC 路径、扩展长度路径、尾随分隔符、`.`/`..` 和保留设备名。
- junction 和其他 reparse point 不等同于普通目录；默认不得跟随出授权根。
- 文件共享模式会影响扫描期间其他程序的访问，应选择真正只读且干扰最小的打开方式。
- 某些读取可能影响访问时间，具体取决于卷设置；不变性验证应明确测试并记录文件系统行为。
- 云同步占位文件可能因读取字节触发下载，因此目标上默认跳过；开启本地哈希也不应隐式拉取占位内容。
- Explorer 定位应使用参数安全的进程调用，不能通过 shell 拼接未可信路径。
- 应用数据库、日志和构建产物必须与用户授权目录分离。
