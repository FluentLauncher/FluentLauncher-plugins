# 插件协议 v1

插件是独立 EXE，与启动器通过 stdin/stdout 传输 JSON-RPC 2.0。每帧为 4 字节大端无符号长度，然后是 UTF-8 JSON；单帧上限 4 MiB。日志只能写 stderr。

## Manifest

`plugin.json` 与 EXE 同目录，包含：`id`、`name`、`version`、`protocolVersion: 1`、`executable`、`keywords`、`permissions`、`description`。ID 只用字母、数字、点、下划线、连字符。executable 是包内文件名，不能带路径。

## 方法

| 方法 | 请求 | 响应 |
| --- | --- | --- |
| `initialize` | 空参数 | `{"protocolVersion":1,"pluginId":"vendor.example"}` |
| `ping` | 空参数 | `"pong"` |
| `query` | `text`、`keyword`、`maxResults`、`queryVersion` | `{"results":[...]}` |
| `execute` | `resultId`、`actionId` | `{}` |
| `$/cancelRequest` | `{"id":<原请求ID>}`，无响应 | 取消对应请求 |

一个结果含 `id`、`title`、`subtitle`、`actions`，可选 `previewText`。每个 action 含 `id`、`title`、`kind`、`value`。

| kind | value / 行为 |
| --- | --- |
| `copyText` | 启动器复制 value，无需插件 execute |
| `openUrl` | 启动器打开 HTTP/HTTPS URL |
| `openPath` | 启动器打开绝对路径 |
| `invoke` | 启动器发 execute，插件按 resultId / actionId 处理 |

查询可能并发，必须响应 context 取消。不要依赖每个查询都会执行动作；全局查询与关键词查询都可能调用同一实例。插件闲置、禁用、卸载或宿主退出时会被结束，持久数据请显式存储，剪贴板示例只用内存。

## Go 示例

最小可运行例子见 `plugins/echo`，模板见 `templates/go-plugin`。第三方可以使用任何语言，只要遵守帧格式、握手与 manifest 约定；无需链接启动器或 MyGo。
