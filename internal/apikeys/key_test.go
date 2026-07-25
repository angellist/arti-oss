package apikeys_test

import (
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/angellist/arti-oss/internal/apikeys"
)

func TestGenerateKey(t *testing.T) {
	plaintext, hash, prefix, err := apikeys.GenerateKey("upload")
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	// plaintext starts with arti_upload_
	if !strings.HasPrefix(plaintext, "arti_upload_") {
		t.Errorf("plaintext %q does not start with arti_upload_", plaintext)
	}

	// prefix is a strict prefix of plaintext
	if !strings.HasPrefix(plaintext, prefix) {
		t.Errorf("prefix %q is not a prefix of plaintext %q", prefix, plaintext)
	}

	// prefix = "arti_upload_" + 5 chars
	const base = "arti_upload_"
	if !strings.HasPrefix(prefix, base) {
		t.Errorf("prefix %q does not start with %q", prefix, base)
	}
	tail := strings.TrimPrefix(prefix, base)
	if len(tail) != 5 {
		t.Errorf("prefix tail len = %d, want 5", len(tail))
	}

	// hash == sha256(plaintext)
	want := sha256.Sum256([]byte(plaintext))
	if len(hash) != 32 {
		t.Errorf("hash len = %d, want 32", len(hash))
	}
	for i, b := range want {
		if hash[i] != b {
			t.Errorf("hash mismatch at byte %d", i)
			break
		}
	}

	// HashKey(plaintext) == hash
	hk := apikeys.HashKey(plaintext)
	for i, b := range hk {
		if hash[i] != b {
			t.Errorf("HashKey mismatch at byte %d", i)
			break
		}
	}

	// two calls differ
	plaintext2, _, prefix2, err2 := apikeys.GenerateKey("upload")
	if err2 != nil {
		t.Fatalf("GenerateKey (2nd): %v", err2)
	}
	if plaintext == plaintext2 {
		t.Error("two GenerateKey calls returned the same plaintext")
	}
	if prefix == prefix2 {
		t.Error("two GenerateKey calls returned the same prefix (collision is possible but extremely unlikely)")
	}
}

func TestIsSupportedScope(t *testing.T) {
	if !apikeys.IsSupportedScope("upload") {
		t.Error("IsSupportedScope(upload) = false, want true")
	}
	if apikeys.IsSupportedScope("full") {
		t.Error("IsSupportedScope(full) = true, want false")
	}
	if apikeys.IsSupportedScope("") {
		t.Error("IsSupportedScope(\"\") = true, want false")
	}
}
