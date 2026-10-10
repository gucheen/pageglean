package config

import (
	"strings"
	"testing"
)

func TestLoadDerivesOrigin(t *testing.T) {
	t.Setenv("PAGEGLEAN_PUBLIC_URL", "https://pageglean.example.com")
	t.Setenv("PAGEGLEAN_DATA_DIR", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicOrigin != "https://pageglean.example.com" {
		t.Fatal("unexpected public origin")
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

func TestWebhookTokenConfiguration(t *testing.T) {
	t.Setenv("PAGEGLEAN_WEBHOOK_URL", "https://hooks.example/notify")
	t.Setenv("PAGEGLEAN_WEBHOOK_SECRET", strings.Repeat("s", 32))
	t.Setenv("PAGEGLEAN_WEBHOOK_TOKEN", "receiver-token_123")
	cfg, err := Load()
	if err != nil || cfg.WebhookToken != "receiver-token_123" {
		t.Fatalf("token configuration: %v", err)
	}
	for _, token := range []string{"secret\r\nInjected: value", " secret", "令牌"} {
		t.Setenv("PAGEGLEAN_WEBHOOK_TOKEN", token)
		if _, err := Load(); err == nil || strings.Contains(err.Error(), token) {
			t.Fatal("invalid token must be rejected without exposing it")
		}
	}
	t.Setenv("PAGEGLEAN_WEBHOOK_TOKEN", "")
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

func TestRivetWebhookConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, mode, url, token string
		valid                  bool
	}{
		{"enabled", "rivet", "https://hooks.example/api/v1/repos/blog/triggers/rebuild", "token", true},
		{"disabled", "rivet", "", "", true},
		{"missing token", "rivet", "https://hooks.example/trigger", "", false},
		{"query", "rivet", "https://hooks.example/trigger?token=secret", "token", false},
		{"credentials", "rivet", "https://user:secret@hooks.example/trigger", "token", false},
		{"invalid token", "rivet", "https://hooks.example/trigger", "bad token", false},
		{"unknown mode", "other", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateWebhookConfig(tc.mode, tc.url, "", tc.token); (err == nil) != tc.valid {
				t.Fatalf("validation: %v", err)
			}
		})
	}
	t.Setenv("PAGEGLEAN_WEBHOOK_MODE", "rivet")
	t.Setenv("PAGEGLEAN_WEBHOOK_URL", "https://hooks.example/api/v1/repos/blog/triggers/rebuild")
	t.Setenv("PAGEGLEAN_WEBHOOK_TOKEN", "token")
	t.Setenv("PAGEGLEAN_WEBHOOK_SECRET", "")
	cfg, err := Load()
	if err != nil || cfg.WebhookMode != "rivet" {
		t.Fatalf("load rivet configuration: %v", err)
	}
}

func TestOIDCConfiguration(t *testing.T) {
	valid := Config{OIDCIssuer: "https://id.example.com/realm", OIDCClientID: "pageglean", OIDCClientSecret: "secret", OIDCOwnerSubject: "owner"}
	for _, issuer := range []string{"https://id.example.com/realm", "http://localhost:9000", "http://127.0.0.1:9000", "http://[::1]:9000"} {
		cfg := valid
		cfg.OIDCIssuer = issuer
		if err := cfg.ValidateOIDC(); err != nil {
			t.Errorf("%s: %v", issuer, err)
		}
	}
	for _, issuer := range []string{"http://id.example.com", "https://user:secret@id.example.com", "https://id.example.com?token=secret", "https://id.example.com#fragment", "file:///tmp/id", ""} {
		cfg := valid
		cfg.OIDCIssuer = issuer
		if err := cfg.ValidateOIDC(); err == nil {
			t.Errorf("accepted %s", issuer)
		} else if strings.Contains(err.Error(), "secret") {
			t.Fatal("validation leaked a secret")
		}
	}
	for _, field := range []string{"client", "secret", "subject"} {
		cfg := valid
		switch field {
		case "client":
			cfg.OIDCClientID = ""
		case "secret":
			cfg.OIDCClientSecret = ""
		case "subject":
			cfg.OIDCOwnerSubject = ""
		}
		if err := cfg.ValidateOIDC(); err == nil {
			t.Errorf("accepted missing %s", field)
		}
	}
	t.Setenv("PAGEGLEAN_OIDC_ISSUER", valid.OIDCIssuer)
	t.Setenv("PAGEGLEAN_OIDC_CLIENT_ID", valid.OIDCClientID)
	t.Setenv("PAGEGLEAN_OIDC_CLIENT_SECRET", valid.OIDCClientSecret)
	t.Setenv("PAGEGLEAN_OIDC_OWNER_SUBJECT", valid.OIDCOwnerSubject)
	cfg, err := Load()
	if err != nil || cfg.ValidateOIDC() != nil || cfg.OIDCOwnerSubject != valid.OIDCOwnerSubject {
		t.Fatalf("load OIDC config: %v", err)
	}
}
