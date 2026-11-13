package main

import (
	"context"
	"testing"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk"
)

func TestQueryReturnsTheTypedText(t *testing.T) {
	results, err := (&echoPlugin{}).Query(context.Background(), pluginsdk.Query{Text: "hello", Keyword: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title != "hello" {
		t.Fatalf("results = %+v", results)
	}
	actions := results[0].Actions
	if len(actions) != 1 || actions[0].Kind != pluginsdk.KindCopyText || actions[0].Value != "hello" {
		t.Fatalf("actions = %+v", actions)
	}
	if results[0].PreviewText != "hello" {
		t.Fatalf("preview = %q", results[0].PreviewText)
	}
}

// With no text the example still answers, so the keyword alone explains itself
// instead of leaving an empty list.
func TestQueryWithoutTextExplainsItself(t *testing.T) {
	results, err := (&echoPlugin{}).Query(context.Background(), pluginsdk.Query{Keyword: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Title == "" {
		t.Fatalf("results = %+v", results)
	}
}

func TestCanceledQueryStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (&echoPlugin{}).Query(ctx, pluginsdk.Query{Text: "x"}); err == nil {
		t.Fatal("a canceled query returned results")
	}
}

func TestExecuteAcceptsTheAction(t *testing.T) {
	if err := (&echoPlugin{}).Execute(context.Background(), pluginsdk.ExecuteRequest{ResultID: "echo", ActionID: "copy"}); err != nil {
		t.Fatal(err)
	}
}
