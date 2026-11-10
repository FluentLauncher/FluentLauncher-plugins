package main

import (
	"context"
	"log"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

type echoPlugin struct{}

func (echoPlugin) Query(ctx context.Context, query pluginsdk.Query) ([]pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	text := query.Text
	if text == "" {
		text = "输入 echo 后跟文本"
	}
	return []pluginsdk.Result{{
		ID:          "echo",
		Title:       text,
		Subtitle:    "官方示例 · 回车复制文本",
		Actions:     []pluginsdk.Action{{ID: "copy", Title: "复制文本", Kind: pluginsdk.KindCopyText, Value: query.Text}},
		PreviewText: query.Text,
	}}, nil
}

func (echoPlugin) Execute(context.Context, pluginsdk.ExecuteRequest) error { return nil }

func main() {
	// The id comes from the plugin.json next to the executable, so it cannot
	// disagree with the manifest.
	if err := pluginsdk.ServeFromManifest(echoPlugin{}); err != nil {
		log.Fatal(err)
	}
}
