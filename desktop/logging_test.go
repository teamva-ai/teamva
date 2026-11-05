package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/logger"
)

func TestDesktopLoggerRedactsSensitiveValues(t *testing.T) {
	message := `nonce=bootstrap-value token:device-value Authorization=Basic-value ` +
		`Bearer bearer-value {"password":"password-value","secret":"secret-value"}`
	redacted := redactLogText(message)
	for _, secret := range []string{"bootstrap-value", "device-value", "Basic-value", "bearer-value", "password-value", "secret-value"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("redacted log still contains %q: %s", secret, redacted)
		}
	}
	if strings.Count(redacted, "[REDACTED]") != 6 {
		t.Fatalf("redacted log=%s", redacted)
	}
}

func TestDesktopLoggerRedactsEscapedAndQuotedCredentials(t *testing.T) {
	for _, test := range []struct {
		name    string
		message string
		want    string
	}{
		{"escaped JSON", `{"password":"test-only-prefix\"test-only-tail","status":"ok"}`, `{"password":"[REDACTED]","status":"ok"}`},
		{"JSON backslash", `{"secret":"test-only-prefix\\test-only-tail"}`, `{"secret":"[REDACTED]"}`},
		{"Basic header", "Authorization: Basic test-only-credential", "Authorization: [REDACTED]"},
		{"Bearer header", "Authorization: Bearer test-only-credential", "Authorization: [REDACTED]"},
		{"Bearer field", "Authorization=Bearer test-only-credential status=ok", "Authorization=[REDACTED] status=ok"},
		{"multiple cookies", "Cookie: am_session=test-only-session; am_remote_41720=test-only-remote", "Cookie: [REDACTED]"},
		{"Digest header", `Authorization: Digest username="test-only-user", response="test-only-proof"`, "Authorization: [REDACTED]"},
		{"quoted field", `password="test only with spaces" status=ok`, `password=[REDACTED] status=ok`},
		{"single quoted field", `secret='test only with spaces' status=ok`, `secret=[REDACTED] status=ok`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if redactLogText(test.message) != test.want {
				t.Fatal("credential value was not completely redacted")
			}
		})
	}
}

func TestRotatingWriterKeepsBoundedPrivateLogs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "app.log")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	writer, err := newRotatingWriter(path, 24, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"first-message\n", "second-message\n", "token=private-value\n", "fourth-message\n"} {
		if _, err := writer.Write([]byte(message)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"app.log", "app.log.1", "app.log.2"} {
		info, err := os.Stat(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatalf("stat %s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s permissions=%o", name, info.Mode().Perm())
		}
		content, err := os.ReadFile(filepath.Join(filepath.Dir(path), name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(content), "private-value") {
			t.Fatalf("%s contains unredacted secret: %s", name, content)
		}
	}
}

var _ logger.Logger = (*desktopLogger)(nil)
