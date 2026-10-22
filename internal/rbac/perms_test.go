package rbac

import (
	"os"
	"strings"
	"testing"
)

// The admin UI names permissions from a map in PermissionMatrix.tsx; a key missing
// there renders as its raw constant.
func TestEveryPermissionHasAWebLabel(t *testing.T) {
	src, err := os.ReadFile("../../web/components/PermissionMatrix.tsx")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range AllPermissions {
		if !strings.Contains(string(src), "  "+string(p)+": \"") {
			t.Errorf("%s has no label in PermissionMatrix.tsx PERM_LABEL", p)
		}
	}
}
