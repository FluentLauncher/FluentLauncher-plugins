package pluginsdk

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FluentLauncher/FluentLauncher-plugins/sdk/internal/rpc"
)

type hostHandler struct {
	mu      sync.RWMutex
	host    Host
	changes atomic.Int32
}

func (h *hostHandler) SettingsChanged(context.Context) error { h.changes.Add(1); return nil }

func (h *hostHandler) SetHost(host Host) { h.mu.Lock(); h.host = host; h.mu.Unlock() }
func (h *hostHandler) Query(ctx context.Context, query Query) ([]Result, error) {
	h.mu.RLock()
	host := h.host
	h.mu.RUnlock()
	if host == nil {
		return []Result{{ID: "old", Title: "legacy"}}, nil
	}
	text, err := ReadClipboardText(ctx, host)
	return []Result{{ID: "host", Title: text}}, err
}
func (*hostHandler) Execute(context.Context, ExecuteRequest) error { return nil }

func TestHostServicesCallbackDuringQueryAndOldHostFallback(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	done := make(chan error, 1)
	handler := &hostHandler{}
	go func() { done <- ServeIO("vendor.example", handler, b, b) }()
	host := rpc.New(a, a, "h:", func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method != "host.clipboard.readText" {
			t.Errorf("unexpected service: %s", method)
		}
		return map[string]string{"text": "nested host callback"}, nil
	})
	defer host.Close(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	call := func(method string, params any) json.RawMessage {
		t.Helper()
		value, err := host.Call(ctx, method, params, 0)
		if err != nil {
			t.Fatal(method, err)
		}
		return value
	}
	var initialized InitializeResult
	if err := json.Unmarshal(call("initialize", nil), &initialized); err != nil {
		t.Fatal(err)
	}
	var legacy QueryResult
	_ = json.Unmarshal(call("query", Query{Text: "old"}), &legacy)
	if legacy.Results[0].Title != "legacy" {
		t.Fatal("old host fallback failed")
	}
	call("configureHost", HostConfiguration{APIVersion: 1, Services: []HostService{{Name: "clipboard.readText", Version: 1}}})
	call("settingsChanged", map[string]any{})
	if handler.changes.Load() != 1 {
		t.Fatal("configuration change event was not delivered")
	}
	var result QueryResult
	_ = json.Unmarshal(call("query", Query{Text: "new"}), &result)
	if len(result.Results) != 1 || result.Results[0].Title != "nested host callback" {
		t.Fatal(result)
	}
	a.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRequiredServicesRejectOldHostBeforeBusinessMethod(t *testing.T) {
	server := &server{pluginID: "vendor.required", handler: &hostHandler{}, required: []HostService{{Name: "settings.get", Version: 1}}}
	if _, err := server.handle(context.Background(), request{Method: "query", Params: json.RawMessage(`{"text":"hello"}`)}); err == nil {
		t.Fatal("required host services were silently ignored")
	}
}
