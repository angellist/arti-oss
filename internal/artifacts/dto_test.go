package artifacts

import (
	"encoding/json"
	"strings"
	"testing"

	sqlc "github.com/angellist/arti-oss/gen/sqlc"
)

// ArtifactInfo.allowed_write must ALWAYS be present in JSON — never omitempty.
// omitempty would drop both nil (mirror) and the empty slice (creator-only),
// collapsing them into an absent field so the client can't tell "write follows
// read" from "creator-only writes" and may re-open writes to readers.
func TestToInfo_AllowedWriteAlwaysSerialized(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string // substring the JSON must contain
	}{
		{"nil mirror serializes as null", nil, `"allowed_write":null`},
		{"empty creator-only serializes as []", []string{}, `"allowed_write":[]`},
		{"explicit list serializes verbatim", []string{"group:eng"}, `"allowed_write":["group:eng"]`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := sqlc.Artifact{Creator: "a@example.com", AllowedAccess: []string{"*"}, AllowedWrite: c.in}
			b, err := json.Marshal(ToInfo(row, "http://x"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), c.want) {
				t.Fatalf("want JSON to contain %s; got %s", c.want, string(b))
			}
		})
	}
}
