package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
)

func TestIssueVerifyAndClassifyFailures(t *testing.T) {
	service, err := Generate("test-key")
	if err != nil {
		t.Fatal(err)
	}
	credential, token, err := service.Issue("project-1", 4, strings.Repeat("a", 64), "publisher")
	if err != nil {
		t.Fatal(err)
	}
	if result := service.Verify(token, "project-1", credential.ManifestDigest, 4); !result.Valid || result.Code != VerificationOK {
		t.Fatalf("valid credential rejected: %#v", result)
	}
	if result := service.Verify(token, "project-1", strings.Repeat("b", 64), 4); result.Code != VerificationDigestMismatch {
		t.Fatalf("expected digest mismatch, got %#v", result)
	}
	if result := service.Verify(token, "project-1", credential.ManifestDigest, 5); result.Code != VerificationStaleVersion {
		t.Fatalf("expected stale version, got %#v", result)
	}
	data, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "CRW1."))
	data[len(data)-2] ^= 1
	tampered := "CRW1." + base64.RawURLEncoding.EncodeToString(data)
	if result := service.Verify(tampered, "", "", 0); result.Valid {
		t.Fatalf("tampered credential accepted: %#v", result)
	}
	_, otherPrivate, _ := ed25519.GenerateKey(nil)
	other, _ := New("other-key", otherPrivate)
	if result := other.Verify(token, "", "", 0); result.Code != VerificationUnknownKey {
		t.Fatalf("expected unknown key, got %#v", result)
	}
}
