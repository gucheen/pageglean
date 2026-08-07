package config

import "testing"

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
