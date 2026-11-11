package pluginsdk

import (
	"context"
	"encoding/json"
	"testing"
)

type themedHandler struct {
	handler
	theme Theme
}

func (h *themedHandler) SetTheme(ctx context.Context, theme Theme) error {
	h.theme = theme
	return ctx.Err()
}

func TestOptionalThemeHandler(t *testing.T) {
	h := &themedHandler{}
	s := &server{pluginID: "vendor.theme", handler: h}
	result, err := s.handle(context.Background(), request{Method: "initialize"})
	if err != nil || result.(InitializeResult).Capabilities[0] != "theme" {
		t.Fatal(result, err)
	}
	theme := Theme{Dark: true, Background: 0x123456, Text: 0xfedcba, Accent: 0x556677, Radius: 9}
	data, _ := json.Marshal(theme)
	if _, err := s.handle(context.Background(), request{Method: "setTheme", Params: data}); err != nil || h.theme != theme {
		t.Fatal("theme round trip", h.theme, err)
	}
	old := &server{pluginID: "vendor.old", handler: &handler{}}
	if _, err := old.handle(context.Background(), request{Method: "setTheme", Params: data}); err == nil {
		t.Fatal("old handler accepted theme")
	}
}
