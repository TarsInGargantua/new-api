package service

import (
	"strings"
	"testing"
)

func TestAPIRequestLogRedactsSecretsWhenEnabled(t *testing.T) {
	body := []byte(`{"api_key":"sk-secret-value","messages":[{"content":"hello"}]}`)
	text, redacted := auditBodyToStringWithRedact(body, "application/json", true)
	if !redacted {
		t.Fatal("expected body to be marked redacted")
	}
	if strings.Contains(text, "sk-secret-value") {
		t.Fatalf("expected secret to be redacted, got %s", text)
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %s", text)
	}
}

func TestAPIRequestLogKeepsSecretsWhenDisabled(t *testing.T) {
	body := []byte(`{"api_key":"sk-secret-value"}`)
	text, redacted := auditBodyToStringWithRedact(body, "application/json", false)
	if redacted {
		t.Fatal("expected body to remain unredacted")
	}
	if !strings.Contains(text, "sk-secret-value") {
		t.Fatalf("expected original secret, got %s", text)
	}
}

func TestAPIRequestLogNonTextContentTypeIsNotAuditable(t *testing.T) {
	if isAuditableContentType("image/png") {
		t.Fatal("image/png should not be auditable")
	}
	if !isAuditableContentType("application/json; charset=utf-8") {
		t.Fatal("json should be auditable")
	}
}
