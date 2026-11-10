package main

import (
	"context"
	"log"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

type handler struct{}

func (handler) Query(ctx context.Context, q pluginsdk.Query) ([]pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []pluginsdk.Result{{ID: "echo", Title: q.Text, Actions: []pluginsdk.Action{{ID: "copy", Title: "复制", Kind: pluginsdk.KindCopyText, Value: q.Text}}}}, nil
}

func (handler) Execute(context.Context, pluginsdk.ExecuteRequest) error { return nil }

func main() {
	if err := pluginsdk.ServeFromManifest(handler{}); err != nil {
		log.Fatal(err)
	}
}
