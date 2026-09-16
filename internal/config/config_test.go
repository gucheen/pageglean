package config

import (
	"strings"
	"testing"
)

func TestLoadDerivesRPID(t *testing.T) {
	t.Setenv("PAGEGLEAN_PUBLIC_URL", "https://pageglean.example.com")
	t.Setenv("PAGEGLEAN_DATA_DIR", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RPID != "pageglean.example.com" {
		t.Fatalf("RPID = %q", cfg.RPID)
	}
	if !cfg.SecureCookies {
		t.Fatal("expected secure cookies for https")
	}
}

func TestLoadRejectsPath(t *testing.T) {
	t.Setenv("PAGEGLEAN_PUBLIC_URL", "https://pageglean.example.com/app")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a public URL with path")
	}
}

func TestWebhookConfiguration(t *testing.T) {
	for _, tc := range []struct {
		url, secret string
		valid       bool
	}{
		{"", "", true},
		{"https://hooks.example/blog", strings.Repeat("s", 32), true},
		{"http://127.0.0.1:9000/blog", strings.Repeat("s", 32), true},
		{"https://hooks.example/blog", "short", false},
		{"", strings.Repeat("s", 32), false},
		{"file:///tmp/job", strings.Repeat("s", 32), false},
		{"https://user:secret@hooks.example", strings.Repeat("s", 32), false},
		{"https://hooks.example/#fragment", strings.Repeat("s", 32), false},
	} {
		if err := ValidateWebhook(tc.url, tc.secret); (err == nil) != tc.valid {
			t.Fatalf("unexpected validation for %s: %v", tc.url, err)
		}
	}
}
