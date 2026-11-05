package store

import "testing"

func TestCSRFTokenIsSessionAndKeyBound(t *testing.T) {
	keyA := []byte("0123456789abcdef0123456789abcdef")
	keyB := []byte("abcdef0123456789abcdef0123456789")
	token := csrfTokenForSession(keyA, "ses_one")
	if token == "" || token != csrfTokenForSession(keyA, "ses_one") {
		t.Fatal("CSRF token must be stable for the same key and session")
	}
	if token == csrfTokenForSession(keyA, "ses_two") {
		t.Fatal("CSRF token must be bound to one session")
	}
	if token == csrfTokenForSession(keyB, "ses_one") {
		t.Fatal("CSRF token must be bound to the server-only key")
	}
	if token == "ses_one" {
		t.Fatal("CSRF token must not disclose the session ID")
	}
}
