package main

import (
	"context"
	"strings"
	"testing"

	"github.com/mistquay/FluentLauncher-plugins/sdk"
)

// The handler answers a routed keyword the way the launcher sends it: the
// keyword in Keyword, the rest in Text.
func TestQueryUsesTheRoutedKeyword(t *testing.T) {
	results, err := devtoolsPlugin{}.Query(context.Background(),
		pluginsdk.Query{Text: "abc", Keyword: "hash", MaxResults: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Title != "SHA-256" {
		t.Fatalf("first result = %+v", results[0])
	}
	if len(results[0].Actions) != 1 || results[0].Actions[0].Kind != pluginsdk.KindCopyText {
		t.Fatalf("actions = %+v", results[0].Actions)
	}
	if !strings.Contains(results[0].Actions[0].Value, "ba7816bf8f01cfea") {
		t.Fatalf("copied value = %q", results[0].Actions[0].Value)
	}
}

// Without a routed keyword the plugin splits the text itself, which is what a
// global query (no keyword) sends.
func TestQuerySplitsTheToolFromTheText(t *testing.T) {
	results, err := devtoolsPlugin{}.Query(context.Background(),
		pluginsdk.Query{Text: "base64 aGVsbG8=", MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].Subtitle == "" {
		t.Fatalf("results = %+v", results)
	}
	for _, result := range results {
		if result.ID == "" || result.Title == "" {
			t.Fatalf("result without identity: %+v", result)
		}
	}
}

// An unknown tool still answers, with the list of the ones it has, instead of
// leaving the user with an empty result list.
func TestUnknownToolExplainsItself(t *testing.T) {
	results, err := devtoolsPlugin{}.Query(context.Background(),
		pluginsdk.Query{Text: "notatool x", MaxResults: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	for _, keyword := range []string{"uuid", "hash"} {
		if !strings.Contains(results[0].Subtitle, keyword) {
			t.Fatalf("subtitle %q does not list %q", results[0].Subtitle, keyword)
		}
	}
}
