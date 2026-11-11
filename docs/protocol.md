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


## 宿主快捷键命令

`plugin.json` 可选 `commands`，用于声明不依赖查询结果的稳定命令。宿主在「设置 → 快捷键」展示；推荐键首次默认禁用，用户显式启用后由宿主注册。插件不自行占用宿主快捷键，也不能覆盖用户改键。

```json
{
  "commands": [
    {
      "id": "show-history",
      "title": "打开历史",
      "description": "打开插件查询",
      "scope": "global",
      "defaultShortcuts": ["Ctrl+Alt+V"],
      "activation": "showQuery",
      "query": "example "
    },
    {
      "id": "capture-region",
      "title": "区域截图",
      "scope": "global",
      "defaultShortcuts": [],
      "activation": "invoke"
    }
  ]
}
```

`scope` 为 `global`（后台可用）或 `search`（搜索面板激活时）。`showQuery` 只由宿主打开并填入查询，不要求插件实现新接口。示例 clipboard、devtools 和模板使用此方式。

`invoke` 要求 SDK 的可选接口 `CommandHandler`：

```go
func (handler) ExecuteCommand(ctx context.Context, req pluginsdk.ExecuteCommandRequest) error {
    switch req.CommandID {
    case "capture-region":
        return startCapture(ctx)
    default:
        return fmt.Errorf("unknown command %q", req.CommandID)
    }
}
```

SDK 仅在实现该接口时，在 initialize 的 `capabilities` 数组声明 `executeCommand`。宿主发送 `executeCommand` RPC，参数 `{"commandId":"capture-region"}`，成功返回 `{}`；未实现时明确报错。原 Handler 接口和协议版本 1 保持不变，旧插件无需修改。

命令 ID 在插件升级中保持稳定。宿主按需启动进程，遵守禁用、暂停、进程上限与更新替换状态；同一命令执行期间忽略重复触发。交互/延时工作须立即返回，完成操作放在插件后台，避免 RPC 超时。现有结果动作仍使用 execute(resultId, actionId)。

此扩展需要发布包含 CommandHandler 的 SDK 标签后，第三方项目再升级依赖。模板当前只用 showQuery，继续兼容已发布的 SDK。

## 交互任务的活动状态

SDK 可选实现 `ActivityHandler.HasActivity(context.Context) (bool, error)`，初始化时自动声明 `activity` 能力。宿主在准备空闲回收前发送 `activity` RPC，响应为 `{"active":true}` 或 `{"active":false}`。

选区、倒计时、贴图窗口（包括隐藏的贴图）等仍存在时返回 true，宿主延后一个空闲周期再检查；无活动返回 false 后回收。不声明此能力的旧插件沿用原行为。检查失败或超时不会无限保活；禁用、卸载、升级、主动停止和宿主退出仍终止进程，且活动插件仍占用现有进程预算。无需主动发送通知，也不改变协议版本 1。

对于采用异步任务的插件，`execute` / `executeCommand` 仅表示任务已接受，不能等待整个交互；异步失败需要由插件自己的界面给出反馈。截图插件的当前版本需要同步更新宿主才能保证长期贴图存活。

## 宿主主题同步

SDK 可选实现 `ThemeHandler.SetTheme(context.Context, Theme) error`，initialize 自动声明 `theme`。宿主在握手后、首次动作前发送 `setTheme`，配色变化（包括系统模式变化）时更新现有进程；插件仅将其排入 GUI 消息队列，不等待绘制。无此能力的旧插件不接收新方法。

可选表面字段 `opacity`（0～1，仅背景）和 `backdrop`（`none` / `acrylic` / `mica`）随同 `setTheme` 发送；缺省不透明、无材质，保持旧宿主兼容。文字和图标不随背景不透明度改变。高对比度传入不透明无材质；Mica 的不透明度固定为 1，由系统管理背景。字段为向后兼容扩展，协议版本仍为 1；插件必须在系统材质不可用时回退主题底色。

`Theme` 传递解析后的 `dark`、`background`、`surface`、`surfaceHover`、`border`、`text`、`textMuted`、`accent`、`accentText`、`danger`、`inverse`、`inverseText`、`radius` 与 `spacing`。颜色为不透明 sRGB 整数 `0xRRGGBB`，JSON 使用十进制数；宿主将透明表面合成到背景色。工具字号由插件保留，搜索面板字号不传入。

主题变更不会启动休眠插件，也不刷新空闲时间。宿主正常停止时先关闭 stdin，给插件最多 400ms 处理 EOF 清理，然后仍通过 Job Object 回收；协议超时与异常停止继续立即结束进程。
