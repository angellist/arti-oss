//go:build integration

package roles_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/rbac"
	"github.com/angellist/arti-oss/internal/roles"
)

func TestAccessRequests_AnnounceOnceAndApproveCreatesAccount(t *testing.T) {
	r, st := setup(t)
	ctx := context.Background()
	email := unique("req") + "@partner.com"
	const admin = "reqadmin@a.com"
	if err := st.AssignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.UnassignRole(ctx, rbac.PrincipalUser, admin, rbac.RoleAdmin) })

	var posts []string
	reqs := roles.AccessRequests{Store: st, Mention: "U123", ReviewURL: "https://arti.test/settings/users",
		Post: func(_ context.Context, text string) error { posts = append(posts, text); return nil }}

	if err := reqs.RequestAccess(ctx, email, "Ada <Lovelace>"); err != nil {
		t.Fatal(err)
	}
	if err := reqs.RequestAccess(ctx, email, "Ada"); err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 {
		t.Fatalf("want one announcement for two submits, got %d", len(posts))
	}
	want := "<@U123> Ada &lt;Lovelace&gt; (" + email + ") has requested an arti account. <https://arti.test/settings/users|Review request>"
	if posts[0] != want {
		t.Errorf("post text:\n got %s\nwant %s", posts[0], want)
	}
	if st.HasAccount(email) {
		t.Fatal("a pending request must not be an account")
	}

	if w := do(t, r, "plain-req@a.com", "POST", "/api/access-requests/approve", `{"email":"`+email+`"}`); w.Code != http.StatusNotFound {
		t.Fatalf("non-admin approve: want 404, got %d", w.Code)
	}
	if st.HasAccount(email) {
		t.Fatal("a refused approval must not create an account")
	}

	w := do(t, r, admin, "GET", "/api/access-requests", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), email) {
		t.Fatalf("admin list: %d %s", w.Code, w.Body.String())
	}
	if w := do(t, r, admin, "POST", "/api/access-requests/approve", `{"email":"`+email+`"}`); w.Code != http.StatusNoContent {
		t.Fatalf("admin approve: %d %s", w.Code, w.Body.String())
	}
	if !st.HasAccount(email) {
		t.Fatal("approval must create an account")
	}
	if w := do(t, r, admin, "POST", "/api/access-requests/decline", `{"email":"`+email+`"}`); w.Code != http.StatusNotFound {
		t.Errorf("a decided request cannot be decided again: got %d", w.Code)
	}
	if s, _ := st.AccessRequestStatus(ctx, email); s != "APPROVED" {
		t.Errorf("status %q, want APPROVED", s)
	}
}
