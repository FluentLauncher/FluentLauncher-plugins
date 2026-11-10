# FluentLauncher 官方插件与开发示例

[插件下载与签名目录](https://github.com/FluentLauncher/FluentLauncher-plugins/releases/latest) · [启动器](https://github.com/FluentLauncher/FluentLauncher)

官方插件独立于启动器分发，启动器只有在用户选择安装或更新时下载对应 ZIP。主程序更新不替换插件。

| 目录 | 用途 |
| --- | --- |
| `sdk/` | 独立 Go module，仅依赖标准库 |
| `plugins/echo/` | 最小查询与复制示例 |
| `plugins/devtools/` | 多关键词、文本工具、多个结果与单元测试 |
| `plugins/clipboard/` | Windows API、内存状态与并发示例 |
| `templates/go-plugin/` | 第三方插件项目模板 |
| `catalog.source.json` | 官方插件的 ID、三语名称与简介 |
| `tools/package/` | 从 manifest 构建 ZIP、生成目录并签名 |

## 本地构建

需要 Windows x64 与 Go 1.27.3，无需完整 Visual Studio。

```powershell
go test ./...
Push-Location sdk
go test ./...
Pop-Location
./scripts/build.ps1
```

输出在 `build/release`，ZIP 根目录包含 `plugin.json` 与插件 EXE。可在启动器“设置 → 插件 → 从 ZIP 安装”中测试。未签名目录仅用于本地开发，正式客户端不会接受。

## 开发自己的插件

1. 将 `templates/go-plugin` 复制到新项目，修改模块名、`plugin.json` 的 ID、名称、关键词。
2. 使用发布的 SDK 模块：`github.com/FluentLauncher/FluentLauncher-plugins/sdk v0.1.0`。维护者应先发布 `sdk/v0.1.0` 标签；本仓库通过根 go.mod 的本地 replace 在发布前开发。
3. 实现 `Query` 与 `Execute`，用 `ServeFromManifest` 启动。SDK 从程序同目录读取 manifest，避免握手 ID 不一致。
4. 在模板目录执行 `go build -o example.exe .`。首次 SDK 版本尚未发布时，可临时 `go mod edit -replace github.com/FluentLauncher/FluentLauncher-plugins/sdk=<本地 SDK 路径>`。
5. 将 EXE 与 `plugin.json` 打 ZIP，使用启动器安装。日志写 stderr，stdout 只能写协议帧。

新 SDK 标签发布后，部分 Go 镜像可能尚未同步；如遇校验服务提示暂不可用，可仅在当前 PowerShell 会话设置 `$env:GOPROXY = 'https://proxy.golang.org,direct'` 后重试 `go mod tidy`。保留模块校验，不需要本地 replace。

完整协议见 [docs/protocol.md](docs/protocol.md)。插件是受信任的本地代码，进程隔离并非安全沙箱。

## 发布

插件的 `plugin.json` 决定插件版本。SDK 单独使用 `sdk/v0.1.0` 等标签；插件集合使用 `v1.0.0` 等标签。每次集合发布携带完整目录与当前插件包，插件各自版本不必一起增加；旧 Release 不覆盖。

正式目录使用独立于启动器更新密钥的 Ed25519 密钥。可用启动器依赖的 `go tool mygo keygen -o <独立密钥目录>` 生成兼容密钥对（32 字节公钥、64 字节私钥，均为 Base64）：

- 本仓库 Repository secret：`FLUENTLAUNCHER_PLUGIN_PRIVATE_KEY`，只供签名使用。
- 本仓库与启动器仓库 Repository variable：`FLUENTLAUNCHER_PLUGIN_PUBLIC_KEY`，填写同一个公钥。
- 启动器 Repository variable：`FLUENTLAUNCHER_PLUGIN_REPOSITORY`，填写本仓库的 `owner/name`。

推送 `v1.0.0` 标签后，Actions 验证源码、构建三个独立 ZIP 和 `catalog.json` / `catalog.sig`，全部上传草稿后才公开 Release。签名覆盖目录原始字节的 SHA-256，目录记录每个 ZIP 的固定版本 URL、大小及 SHA-256。

目录刷新只下载元数据；点击安装才下载可执行文件。私钥不要提交、不要与主程序更新私钥混用；备份原密钥，后续版本持续使用。
