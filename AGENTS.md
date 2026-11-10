# 插件仓库协作约定

- 使用简体中文交流，文本统一 UTF-8 无 BOM。
- SDK 是独立 Go module，只依赖标准库，禁止引入 MyGo 或启动器宿主代码。
- 插件是受信任的独立进程；stdout 只写 JSON-RPC 帧，日志写 stderr。
- 插件 ID、协议版本和启动文件必须与 plugin.json 一致，示例使用 ServeFromManifest。
- 依赖固定在 go.mod。完成后在根目录及 sdk 目录运行 go build ./...、go vet ./...、go test ./...。
- 根目录脚本 build.ps1 生成 ZIP 和目录；正式发布目录必须使用插件仓库独立密钥签名。
- 修改完成后创建本地提交，未经用户单独授权不推送，不更改启动器的数据。
