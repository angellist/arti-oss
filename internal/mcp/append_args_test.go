package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The argument checks in toolAppend run before the service is touched, so a
// zero Server covers them without a database; the alias's happy path is in the
// integration suite (TestAppendArtifactAcceptsIdentAlias).
func TestToolAppendArgumentErrors(t *testing.T) {
	s := &Server{}
	for _, tc := range []struct {
		name, args, want string
	}{
		{"no target", `{"content":"x"}`, "append_artifact takes `slug`"},
		{"slug and ident disagree", `{"slug":"doc-a","ident":"doc-b","content":"x"}`, "send one of them"},
		{"artifact id as ident", `{"ident":"6f1c2b8e-1d2a-4c3b-9e8f-0a1b2c3d4e5f","content":"x"}`, "needs the document's slug"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.toolAppend(context.Background(), json.RawMessage(tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
