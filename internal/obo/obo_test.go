package obo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/angellist/arti-oss/internal/apps"
)

// Broker must satisfy the apps.TokenProvider interface.
var _ apps.TokenProvider = (*Broker)(nil)

type memStore struct {
	clients map[string]ClientReg
	tokens  map[string]TokenRec
}

func newMemStore() *memStore {
	return &memStore{clients: map[string]ClientReg{}, tokens: map[string]TokenRec{}}
}
func (m *memStore) GetClient(_ context.Context, issuer string) (ClientReg, bool, error) {
	c, ok := m.clients[issuer]
	return c, ok, nil
}
func (m *memStore) PutClient(_ context.Context, issuer string, c ClientReg) error {
	if _, ok := m.clients[issuer]; !ok { // ON CONFLICT DO NOTHING
		m.clients[issuer] = c
	}
	return nil
}
func (m *memStore) GetToken(_ context.Context, email, resource string) (TokenRec, bool, error) {
	t, ok := m.tokens[email+"|"+resource]
	return t, ok, nil
}
func (m *memStore) PutToken(_ context.Context, email, resource string, t TokenRec) error {
	m.tokens[email+"|"+resource] = t
	return nil
}

func testCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher([]byte("unit-test-master-key-0123456789ab"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// A shared authorize_url must not let a different user complete the consent and
// bind THEIR token to the initiator. The callback resolves the browser identity
// and, if it doesn't match the email sealed in the state, rejects before any
// token exchange and stores nothing.
func TestCallback_RejectsDifferentUser(t *testing.T) {
	cipher := testCipher(t)
	store := newMemStore()
	b := New("https://arti.example.com", func(r *http.Request) string {
		return strings.TrimSpace(r.Header.Get("X-Auth-Request-Email"))
	}, store, cipher)

	const initiator = "alice@example.com"
	const resource = "https://gateway.example.com/api/v1/proxy/X/mcp"
	state, err := cipher.SealState(authState{Email: initiator, Verifier: "v", Resource: resource, IAT: time.Now().Unix()})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/oauth/obo/callback?state="+url.QueryEscape(state)+"&code=abc", nil)
	req.Header.Set("X-Auth-Request-Email", "bob@example.com") // a different user
	rec := httptest.NewRecorder()
	b.Callback(rec, req)

	if !strings.Contains(rec.Body.String(), "different user") {
		t.Fatalf("expected rejection page, got: %s", rec.Body.String())
	}
	if _, ok := store.tokens[initiator+"|"+resource]; ok {
		t.Fatal("must NOT have stored a token for the initiator after a mismatched consent")
	}
}

// A correctly-signed but expired state token is rejected (TTL), before any
// network call or token storage.
func TestCallback_RejectsExpiredState(t *testing.T) {
	cipher := testCipher(t)
	b := New("https://arti.example.com", nil, newMemStore(), cipher)
	old := time.Now().Add(-20 * time.Minute).Unix()
	state, _ := cipher.SealState(authState{Email: "a@x.com", Verifier: "v", Resource: "https://h/mcp", IAT: old})

	req := httptest.NewRequest(http.MethodGet, "/oauth/obo/callback?state="+url.QueryEscape(state)+"&code=abc", nil)
	rec := httptest.NewRecorder()
	b.Callback(rec, req)
	if !strings.Contains(rec.Body.String(), "invalid or expired") {
		t.Fatalf("expected expiry rejection, got: %s", rec.Body.String())
	}
}

// A garbage / wrong-key state token is rejected (forgery protection).
func TestCallback_RejectsForgedState(t *testing.T) {
	b := New("https://arti.example.com", nil, newMemStore(), testCipher(t))
	req := httptest.NewRequest(http.MethodGet, "/oauth/obo/callback?state=not-a-real-token&code=abc", nil)
	rec := httptest.NewRecorder()
	b.Callback(rec, req)
	if !strings.Contains(rec.Body.String(), "invalid or expired") {
		t.Fatalf("expected forged-state rejection, got: %s", rec.Body.String())
	}
}
