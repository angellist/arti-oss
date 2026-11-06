//go:build integration

package pgstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/angellist/arti-oss/internal/store/pgstore"
)

func TestHasAccount_OnlyAdminAndLoginRowsAdmit(t *testing.T) {
	st, pool := rosterStore(t, pgstore.Config{})
	ctx := context.Background()
	id := uuid.NewString()[:8]
	back, login, added, off, revived := "back-"+id+"@partner.com", "login-"+id+"@partner.com", "added-"+id+"@partner.com", "off-"+id+"@partner.com", "revived-"+id+"@gmail.com"
	mustExec(t, pool, `INSERT INTO users (email, source) VALUES ($1, 'backfill'), ($2, 'login')`, back, login)
	if _, err := st.AddUser(ctx, added, "", "", "admin@a.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, off, "", "", "admin@a.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeactivateUser(ctx, off, "admin@a.com"); err != nil {
		t.Fatal(err)
	}
	// Deactivating an unknown address creates a row nobody added; reactivating
	// it must not turn that row into an account.
	if err := st.DeactivateUser(ctx, revived, "admin@a.com"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReactivateUser(ctx, revived); err != nil {
		t.Fatal(err)
	}

	for email, want := range map[string]bool{back: false, login: true, added: true, off: false, revived: false, "none-" + id + "@partner.com": false} {
		if got := st.HasAccount(email); got != want {
			t.Errorf("HasAccount(%s) = %v, want %v", email, got, want)
		}
	}
	if _, isAdded := st.Account(login); isAdded {
		t.Error("a login row must not read as admin-added")
	}
	if _, isAdded := st.Account(added); !isAdded {
		t.Error("an AddUser row must read as admin-added")
	}
}

func TestCreateAccessRequest_SecondSubmitIsNotNew(t *testing.T) {
	st, _ := rosterStore(t, pgstore.Config{})
	ctx := context.Background()
	email := "dup-" + uuid.NewString()[:8] + "@partner.com"
	first, err := st.CreateAccessRequest(ctx, email, "Ada")
	if err != nil || !first {
		t.Fatalf("first: %v %v", first, err)
	}
	if err := st.DecideAccessRequest(ctx, email, false, "admin@a.com"); err != nil {
		t.Fatal(err)
	}
	again, err := st.CreateAccessRequest(ctx, email, "Ada")
	if err != nil || again {
		t.Fatalf("a declined person re-asking must not reopen: %v %v", again, err)
	}
	if s, _ := st.AccessRequestStatus(ctx, email); s != pgstore.AccessRequestDenied {
		t.Errorf("status %q, want DENIED", s)
	}
	if st.HasAccount(email) {
		t.Error("a declined request must not create an account")
	}
}
