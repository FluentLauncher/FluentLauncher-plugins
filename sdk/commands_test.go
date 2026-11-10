package pluginsdk

import (
	"context"
	"encoding/json"
	"testing"
)

type commandHandler struct {
	handler
	command string
}

func (h *commandHandler) ExecuteCommand(ctx context.Context, req ExecuteCommandRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	h.command = req.CommandID
	return nil
}

func TestOptionalCommands(t *testing.T) {
	old := &server{pluginID: "vendor.old", handler: &handler{}}
	initialized, err := old.handle(context.Background(), request{Method: "initialize"})
	if err != nil || len(initialized.(InitializeResult).Capabilities) != 0 {
		t.Fatalf("old initialization: %+v %v", initialized, err)
	}
	if _, err := old.handle(context.Background(), request{Method: "executeCommand", Params: json.RawMessage(`{"commandId":"capture"}`)}); err == nil {
		t.Fatal("old handler accepted command")
	}
	h := &commandHandler{}
	modern := &server{pluginID: "vendor.modern", handler: h}
	initialized, err = modern.handle(context.Background(), request{Method: "initialize"})
	if err != nil || len(initialized.(InitializeResult).Capabilities) != 1 {
		t.Fatalf("capabilities: %+v %v", initialized, err)
	}
	if _, err := modern.handle(context.Background(), request{Method: "executeCommand", Params: json.RawMessage(`{"commandId":"capture"}`)}); err != nil || h.command != "capture" {
		t.Fatalf("command: %s %v", h.command, err)
	}
	if _, err := modern.handle(context.Background(), request{Method: "executeCommand", Params: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("empty command accepted")
	}
}
