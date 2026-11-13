package pluginsdk

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk/internal/rpc"
)

// HostAware opts in without changing the original Handler interface. Older
// hosts never call SetHost: optional functionality must handle a nil Host.
type HostAware interface{ SetHost(Host) }

// SettingsHandler receives configuration changes while the process is alive.
// Refresh with settings.get; enqueue UI work rather than waiting for painting.
type SettingsHandler interface{ SettingsChanged(context.Context) error }

type Host interface {
	Supports(service string, version int) bool
	Configuration() HostConfiguration
	Call(context.Context, string, any, any) error
}

type HostService struct {
	Name    string `json:"name"`
	Version int    `json:"version"`
}
type HostConfiguration struct {
	APIVersion  int           `json:"apiVersion"`
	HostVersion string        `json:"hostVersion"`
	Locale      string        `json:"locale"`
	DataDir     string        `json:"dataDir"`
	CacheDir    string        `json:"cacheDir"`
	Services    []HostService `json:"services"`
}

type RPCError = rpc.Error

type hostClient struct {
	peer          *rpc.Peer
	configuration HostConfiguration
}

func (h *hostClient) Supports(service string, version int) bool {
	for _, available := range h.configuration.Services {
		if available.Name == service && available.Version >= version {
			return true
		}
	}
	return false
}
func (h *hostClient) Configuration() HostConfiguration {
	configuration := h.configuration
	configuration.Services = append([]HostService(nil), configuration.Services...)
	return configuration
}
func (h *hostClient) Call(ctx context.Context, method string, params any, result any) error {
	if !h.Supports(method, 1) {
		return &RPCError{Code: -32601, Message: "host service is unavailable", Data: map[string]string{"kind": "unsupported"}}
	}
	raw, err := h.peer.Call(ctx, "host."+method, params, 0)
	if err != nil {
		return err
	}
	if result != nil {
		return json.Unmarshal(raw, result)
	}
	return nil
}

type Notice struct {
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Message   string `json:"message,omitempty"`
	ReplaceID string `json:"replaceId,omitempty"`
}

func Notify(ctx context.Context, host Host, notice Notice) error {
	if host == nil {
		return errors.New("host services require a newer launcher")
	}
	return host.Call(ctx, "ui.notify", notice, nil)
}

func ReadClipboardText(ctx context.Context, host Host) (string, error) {
	if host == nil {
		return "", errors.New("host services require a newer launcher")
	}
	var result struct {
		Text string `json:"text"`
	}
	err := host.Call(ctx, "clipboard.readText", nil, &result)
	return result.Text, err
}

func WriteClipboardText(ctx context.Context, host Host, text string) error {
	if host == nil {
		return errors.New("host services require a newer launcher")
	}
	return host.Call(ctx, "clipboard.writeText", map[string]string{"text": text}, nil)
}

func ShowQuery(ctx context.Context, host Host, query string) error {
	if host == nil {
		return errors.New("host services require a newer launcher")
	}
	return host.Call(ctx, "launcher.show", map[string]string{"query": query}, nil)
}
