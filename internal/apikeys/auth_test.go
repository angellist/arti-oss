package apikeys_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/angellist/arti-oss/gen/sqlc"
	"github.com/angellist/arti-oss/internal/apikeys"
	"github.com/angellist/arti-oss/internal/auth"
)

// fakeAuthStore implements the authStore interface for testing.
type fakeAuthStore struct {
	key sqlc.ApiKey
	err error
}

func (f *fakeAuthStore) GetAPIKeyByHash(_ context.Context, _ []byte) (sqlc.ApiKey, error) {
	return f.key, f.err
}

func (f *fakeAuthStore) TouchAPIKey(_ context.Context, _ pgtype.UUID) error {
	return nil
}

func TestAuthenticator_ValidUploadKey(t *testing.T) {
	plaintext, hash, _, err := apikeys.GenerateKey("upload")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	_ = hash

	store := &fakeAuthStore{
		key: sqlc.ApiKey{
			OwnerEmail: "alice@example.com",
			Scopes:     []string{"upload"},
		},
	}
	a := apikeys.NewAuthenticator(store)
	claims, err := a.Authenticate(context.Background(), plaintext)
	if err != nil {
		t.Fatalf("Authenticate: unexpected error: %v", err)
	}
	if claims.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", claims.Email, "alice@example.com")
	}
	if len(claims.Scopes) != 1 || claims.Scopes[0] != "upload" {
		t.Errorf("Scopes = %v, want [upload]", claims.Scopes)
	}
	if claims.Typ != "api-key" {
		t.Errorf("Typ = %q, want %q", claims.Typ, "api-key")
	}
}

func TestAuthenticator_StoreError(t *testing.T) {
	plaintext, _, _, err := apikeys.GenerateKey("upload")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	store := &fakeAuthStore{err: errors.New("not found")}
	a := apikeys.NewAuthenticator(store)
	_, err = a.Authenticate(context.Background(), plaintext)
	if err == nil {
		t.Fatal("Authenticate: expected error for store failure, got nil")
	}
}

func TestAuthenticator_UnsupportedScope_FailClosed(t *testing.T) {
	plaintext, _, _, err := apikeys.GenerateKey("upload")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// Row has an unsupported scope — must be rejected (fail-closed).
	store := &fakeAuthStore{
		key: sqlc.ApiKey{
			OwnerEmail: "alice@example.com",
			Scopes:     []string{"full"},
		},
	}
	a := apikeys.NewAuthenticator(store)
	_, err = a.Authenticate(context.Background(), plaintext)
	if err == nil {
		t.Fatal("Authenticate: expected error for unsupported scope, got nil")
	}
}

func TestAuthenticator_EmptyScopes_FailClosed(t *testing.T) {
	plaintext, _, _, err := apikeys.GenerateKey("upload")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// A row with no scopes must be rejected — an empty scope set would otherwise
	// leave the key non-upload-scoped and silently bypass EnforceUploadScope.
	store := &fakeAuthStore{
		key: sqlc.ApiKey{
			OwnerEmail: "alice@example.com",
			Scopes:     []string{},
		},
	}
	a := apikeys.NewAuthenticator(store)
	if _, err = a.Authenticate(context.Background(), plaintext); err == nil {
		t.Fatal("Authenticate: expected error for empty-scope key, got nil")
	}
}

func TestAuthenticator_WrongPrefix(t *testing.T) {
	store := &fakeAuthStore{
		key: sqlc.ApiKey{
			OwnerEmail: "alice@example.com",
			Scopes:     []string{"upload"},
		},
	}
	a := apikeys.NewAuthenticator(store)
	_, err := a.Authenticate(context.Background(), "eyJhbGciOiJIUzI1NiJ9.foo.bar")
	if !errors.Is(err, auth.ErrMalformed) {
		t.Errorf("expected ErrMalformed for non-arti_ token, got %v", err)
	}
}
