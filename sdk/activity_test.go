package pluginsdk

import (
	"context"
	"testing"
)

type activityHandler struct {
	handler
	active bool
}

func (h *activityHandler) HasActivity(context.Context) (bool, error) { return h.active, nil }

func TestActivityCapabilityAndHiddenWork(t *testing.T) {
	h := &activityHandler{active: true}
	s := &server{pluginID: "vendor.ui", handler: h}
	result, err := s.handle(context.Background(), request{Method: "initialize"})
	if err != nil {
		t.Fatal(err)
	}
	caps := result.(InitializeResult).Capabilities
	if len(caps) != 1 || caps[0] != "activity" {
		t.Fatalf("capabilities: %v", caps)
	}
	for _, active := range []bool{true, false} {
		h.active = active
		result, err = s.handle(context.Background(), request{Method: "activity"})
		if err != nil || result.(ActivityResult).Active != active {
			t.Fatalf("activity: %+v %v", result, err)
		}
	}
	old := &server{pluginID: "vendor.old", handler: &handler{}}
	if _, err := old.handle(context.Background(), request{Method: "activity"}); err == nil {
		t.Fatal("old handler accepted activity")
	}
}
