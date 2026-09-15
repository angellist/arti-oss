package comments

import (
	"testing"

	"github.com/google/uuid"
)

func TestCommentWriteRateLimitPerPrincipalAndArtifact(t *testing.T) {
	s := NewService(nil, nil, nil)
	s.SetRateLimit(2, 1)
	a := uuid.New()
	b := uuid.New()

	if !s.allowWrite("alice@example.com", a) {
		t.Fatal("first write should pass")
	}
	if s.allowWrite("alice@example.com", a) {
		t.Fatal("second write on same artifact should hit artifact cap")
	}
	if !s.allowWrite("alice@example.com", b) {
		t.Fatal("same principal should still have global budget on a different artifact")
	}
	if s.allowWrite("alice@example.com", uuid.New()) {
		t.Fatal("third write should hit global cap")
	}
	if !s.allowWrite("bob@example.com", a) {
		t.Fatal("a different principal should have its own bucket")
	}
}

func TestCommentWriteRateLimitCanBeDisabled(t *testing.T) {
	s := NewService(nil, nil, nil)
	s.SetRateLimit(0, 0)
	a := uuid.New()
	for i := 0; i < 5; i++ {
		if !s.allowWrite("alice@example.com", a) {
			t.Fatalf("write %d should pass with limits disabled", i)
		}
	}
}

func TestCommentWriteRateLimitGlobalCanBeDisabled(t *testing.T) {
	s := NewService(nil, nil, nil)
	s.SetRateLimit(0, 1)
	a := uuid.New()
	b := uuid.New()

	if !s.allowWrite("alice@example.com", a) {
		t.Fatal("first write should pass")
	}
	if s.allowWrite("alice@example.com", a) {
		t.Fatal("second write on same artifact should hit artifact cap")
	}
	if !s.allowWrite("alice@example.com", b) {
		t.Fatal("global disabled should allow a different artifact after an artifact cap")
	}
}
