package apikeys_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/auth"
)

const servicePrincipal = "couch@sa.arti.invalid"

// A service principal cannot log in, so its key is minted by an admin from a
// browser session; the key is the service scope's only source, and it records
// who minted it.
func TestAdminMintsAServicePrincipalKey(t *testing.T) {
	store := &fakeServiceStore{services: map[string]bool{servicePrincipal: true}}
	r := newRouter(store, 365*24*time.Hour)
	body := `{"owner_email":"COUCH@sa.arti.invalid","name":"couch-prod"}`

	if w := do(t, r, "alice@example.com", "POST", "/api/admin/service-keys", body); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin mint: %d, want 403", w.Code)
	}
	bearer := auth.Credential{Kind: auth.CredKindToken}
	if w := doAs(t, r, adminEmail, bearer, "POST", "/api/admin/service-keys", body); w.Code != http.StatusForbidden {
		t.Fatalf("admin mint from a bearer token: %d, want 403", w.Code)
	}
	if w := do(t, r, adminEmail, "POST", "/api/admin/service-keys", `{"owner_email":"bob@example.com","name":"x"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("admin minting for a person: %d, want 400", w.Code)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("refused mints stored %d keys", len(store.inserted))
	}

	w := do(t, r, adminEmail, "POST", "/api/admin/service-keys", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin mint: %d %s", w.Code, w.Body.String())
	}
	got := store.inserted[0]
	if got.OwnerEmail != servicePrincipal || len(got.Scopes) != 1 || got.Scopes[0] != auth.ServiceScope {
		t.Fatalf("stored owner=%q scopes=%v, want %q with the service scope", got.OwnerEmail, got.Scopes, servicePrincipal)
	}
	if got.MintedBy == nil || *got.MintedBy != adminEmail {
		t.Fatalf("minted_by = %v, want %q", got.MintedBy, adminEmail)
	}
}

// Self-serve keys never carry the service scope, whoever asks.
func TestSelfServeMintRefusesTheServiceScope(t *testing.T) {
	store := &fakeServiceStore{}
	r := newRouter(store, 365*24*time.Hour)
	if w := do(t, r, adminEmail, "POST", "/api/keys", `{"name":"x","scopes":["service"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("self-serve service key: %d, want 400", w.Code)
	}
	if len(store.inserted) != 0 {
		t.Fatal("a self-serve service key was stored")
	}
}
