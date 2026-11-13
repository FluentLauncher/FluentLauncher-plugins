package main

import (
	"context"
	"log"
	"sync"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

type echoPlugin struct {
	mu   sync.RWMutex
	host pluginsdk.Host
}

func (p *echoPlugin) SetHost(host pluginsdk.Host) { p.mu.Lock(); p.host = host; p.mu.Unlock() }
func (p *echoPlugin) currentHost() pluginsdk.Host { p.mu.RLock(); defer p.mu.RUnlock(); return p.host }

func (p *echoPlugin) Query(ctx context.Context, query pluginsdk.Query) ([]pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := query.Text
	if text == "" {
		text = "输入 echo 后跟文本"
	}
	preview := query.Text
	host := p.currentHost()
	if host != nil && host.Supports("settings.get", 1) {
		var settings struct {
			Values struct {
				Prefix  string `json:"prefix"`
				Preview bool   `json:"preview"`
			} `json:"values"`
		}
		if err := host.Call(ctx, "settings.get", nil, &settings); err != nil {
			return nil, err
		}
		text = settings.Values.Prefix + text
		if !settings.Values.Preview {
			preview = ""
		}
	}
	actions := []pluginsdk.Action{{ID: "copy", Title: "复制文本", Kind: pluginsdk.KindCopyText, Value: query.Text}}
	if host != nil && host.Supports("ui.notify", 1) {
		actions = append(actions, pluginsdk.Action{ID: "notify", Title: "宿主通知示例", Kind: pluginsdk.KindInvoke})
	}
	return []pluginsdk.Result{{
		ID:          "echo",
		Title:       text,
		Subtitle:    "官方示例 · 回车复制文本",
		Actions:     actions,
		PreviewText: preview,
	}}, nil
}

func (p *echoPlugin) Execute(ctx context.Context, request pluginsdk.ExecuteRequest) error {
	if request.ActionID == "notify" {
		return pluginsdk.Notify(ctx, p.currentHost(), pluginsdk.Notice{Kind: "success", Title: "插件已连接宿主", Message: "这条提示由宿主通知服务发送。", ReplaceID: "echo-demo"})
	}
	return nil
}

func main() {
	// The id comes from the plugin.json next to the executable, so it cannot
	// disagree with the manifest.
	if err := pluginsdk.ServeFromManifest(&echoPlugin{}); err != nil {
		log.Fatal(err)
	}
}
