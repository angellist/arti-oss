package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/angellist/arti-oss/internal/audit"
	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func TestNoteToolCallRecordsIdentAndOutcome(t *testing.T) {
	ctx, e := audit.WithEntry(context.Background())
	noteToolCall(ctx, "read_artifact", json.RawMessage(`{"ident":"doc-a"}`), nil)
	noteToolCall(ctx, "append_artifact", json.RawMessage(`{"slug":"doc-b"}`), pgstore.ErrNotFound)
	noteToolCall(ctx, "add_artifact", json.RawMessage(`{"named_slug":"doc-c"}`), errors.New("boom"))

	var ev audit.Event
	e.Fill(&ev)
	want := []audit.ToolCall{
		{Name: "read_artifact", Ident: "doc-a", Outcome: "allowed"},
		{Name: "append_artifact", Ident: "doc-b", Outcome: "denied"},
		{Name: "add_artifact", Ident: "doc-c", Outcome: "error"},
	}
	if len(ev.Tools) != len(want) {
		t.Fatalf("tools = %+v", ev.Tools)
	}
	for i := range want {
		if ev.Tools[i] != want[i] {
			t.Errorf("tool %d = %+v, want %+v", i, ev.Tools[i], want[i])
		}
	}
}
