package pluginsdk

import "context"

// Theme carries resolved opaque sRGB colors, encoded as 0xRRGGBB. No host UI
// package is needed. Text size is controlled by the plugin, independent of the
// launcher's search-panel font size.
type Theme struct {
	Dark         bool    `json:"dark"`
	Background   uint32  `json:"background"`
	Surface      uint32  `json:"surface"`
	SurfaceHover uint32  `json:"surfaceHover"`
	Border       uint32  `json:"border"`
	Text         uint32  `json:"text"`
	TextMuted    uint32  `json:"textMuted"`
	Accent       uint32  `json:"accent"`
	AccentText   uint32  `json:"accentText"`
	Danger       uint32  `json:"danger"`
	Inverse      uint32  `json:"inverse"`
	InverseText  uint32  `json:"inverseText"`
	Radius       float32 `json:"radius"`
	Spacing      float32 `json:"spacing"`
}

// ThemeHandler is optional. Return after queuing the palette on the GUI thread.
// The host sends the current palette after initialize and on theme changes.
type ThemeHandler interface {
	SetTheme(context.Context, Theme) error
}
