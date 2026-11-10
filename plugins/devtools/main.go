package main

import (
	"context"
	"log"
	"strings"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

type devtoolsPlugin struct{}

func (devtoolsPlugin) Query(ctx context.Context, query pluginsdk.Query) ([]pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keyword := query.Keyword
	input := query.Text
	if keyword == "" {
		keyword, input = splitTool(query.Text)
	}
	outputs := run(keyword, input)
	limit := max(1, query.MaxResults)
	if len(outputs) > limit {
		outputs = outputs[:limit]
	}
	results := make([]pluginsdk.Result, 0, len(outputs))
	for i, item := range outputs {
		result := pluginsdk.Result{
			ID:       keyword + ":" + itoa(i),
			Title:    item.Title,
			Subtitle: subtitle(item),
		}
		if item.Text != "" {
			result.PreviewText = item.Text
			result.Actions = []pluginsdk.Action{{ID: "copy", Title: "复制结果", Kind: pluginsdk.KindCopyText, Value: item.Text}}
		}
		results = append(results, result)
	}
	return results, nil
}

func (devtoolsPlugin) Execute(context.Context, pluginsdk.ExecuteRequest) error { return nil }

func splitTool(text string) (string, string) {
	trimmed := strings.TrimSpace(text)
	index := strings.IndexByte(trimmed, ' ')
	if index < 0 {
		return trimmed, ""
	}
	return trimmed[:index], trimmed[index+1:]
}

func subtitle(item toolOutput) string {
	text := strings.NewReplacer("\r\n", " ⏎ ", "\n", " ⏎ ", "\r", " ⏎ ").Replace(item.Text)
	if text == "" {
		return item.Subtitle
	}
	runes := []rune(text)
	if len(runes) <= 120 {
		return text + " · " + item.Subtitle
	}
	return string(runes[:120]) + "… · " + item.Subtitle
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	pos := len(digits)
	for value > 0 {
		pos--
		digits[pos] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[pos:])
}

func main() {
	if err := pluginsdk.ServeFromManifest(devtoolsPlugin{}); err != nil {
		log.Fatal(err)
	}
}
