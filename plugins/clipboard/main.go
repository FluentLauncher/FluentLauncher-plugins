package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mistquay/FluentLauncher-plugins/sdk"
)

// history is the process-wide clipboard ring; the watcher fills it, queries
// read it. It lives only in memory and dies with the process.
var history = newStore()

type handler struct{}

func (handler) Query(ctx context.Context, query pluginsdk.Query) ([]pluginsdk.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	items := history.Snapshot(query.Text)
	limit := query.MaxResults
	if limit <= 0 {
		limit = 10
	}
	if len(items) > limit {
		items = items[:limit]
	}
	results := make([]pluginsdk.Result, 0, len(items))
	for i, it := range items {
		results = append(results, pluginsdk.Result{
			ID:          strconv.Itoa(i),
			Title:       clipTitle(it.Text),
			Subtitle:    fmt.Sprintf("%d 字符 · %s", utf8.RuneCountInString(it.Text), it.At.Format("15:04")),
			Actions:     []pluginsdk.Action{{ID: "copy", Title: "复制文本", Kind: pluginsdk.KindCopyText, Value: it.Text}},
			PreviewText: it.Text,
		})
	}
	if len(results) == 0 && query.Text == "" {
		results = []pluginsdk.Result{{
			ID:       "empty",
			Title:    "还没有剪贴板历史",
			Subtitle: "复制过的文本会自动出现在这里（仅内存、最多 50 条）",
		}}
	}
	return results, nil
}

func (handler) Execute(context.Context, pluginsdk.ExecuteRequest) error { return nil }

// clipTitle is the text's first line, trimmed and cut to 80 runes.
func clipTitle(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 80 {
		return string(runes[:80]) + "…"
	}
	return text
}

func main() {
	StartWatch(history.Add)
	if err := pluginsdk.ServeFromManifest(handler{}); err != nil {
		log.Fatal(err)
	}
}
