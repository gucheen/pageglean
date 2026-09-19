package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	WebhookMode       string
	WebhookURL        string
	WebhookSecret     string
	WebhookToken      string
	Addr              string
	PublicURL         string
	PublicOrigin      string
	RPID              string
	DataDir           string
	DatabasePath      string
	SecureCookies     bool
	AllowPrivateFetch bool
}

func Load() (Config, error) {
	publicURL := envOr("PAGEGLEAN_PUBLIC_URL", "http://localhost:8080")
	parsed, err := url.Parse(publicURL)
	if err != nil {
		return Config{}, fmt.Errorf("parse PAGEGLEAN_PUBLIC_URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return Config{}, fmt.Errorf("PAGEGLEAN_PUBLIC_URL must use http or https")
	}
	if parsed.Hostname() == "" {
		return Config{}, fmt.Errorf("PAGEGLEAN_PUBLIC_URL must include a hostname")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return Config{}, fmt.Errorf("PAGEGLEAN_PUBLIC_URL must not include a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return Config{}, fmt.Errorf("PAGEGLEAN_PUBLIC_URL must be an origin without credentials, query, or fragment")
	}

	rpID := strings.TrimSpace(os.Getenv("PAGEGLEAN_RP_ID"))
	if rpID == "" {
		rpID = parsed.Hostname()
	}
	dataDir := envOr("PAGEGLEAN_DATA_DIR", "./data")
	absDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve PAGEGLEAN_DATA_DIR: %w", err)
	}
	webhookMode := envOr("PAGEGLEAN_WEBHOOK_MODE", "generic")
	webhookURL := strings.TrimSpace(os.Getenv("PAGEGLEAN_WEBHOOK_URL"))
	webhookSecret := os.Getenv("PAGEGLEAN_WEBHOOK_SECRET")
	webhookToken := os.Getenv("PAGEGLEAN_WEBHOOK_TOKEN")
	if err := ValidateWebhookConfig(webhookMode, webhookURL, webhookSecret, webhookToken); err != nil {
		return Config{}, err
	}
	origin := parsed.Scheme + "://" + parsed.Host
	return Config{
		WebhookMode: webhookMode, WebhookURL: webhookURL, WebhookSecret: webhookSecret, WebhookToken: webhookToken, Addr: envOr("PAGEGLEAN_ADDR", ":8080"),
		PublicURL:         strings.TrimRight(origin, "/"),
		PublicOrigin:      origin,
		RPID:              rpID,
		DataDir:           absDataDir,
		DatabasePath:      filepath.Join(absDataDir, "pageglean.db"),
		SecureCookies:     parsed.Scheme == "https",
		AllowPrivateFetch: strings.EqualFold(strings.TrimSpace(os.Getenv("PAGEGLEAN_ALLOW_PRIVATE_FETCH")), "true"),
	}, nil
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func ValidateWebhook(endpoint, secret string) error {
	if endpoint == "" && secret == "" {
		return nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Fragment != "" {
		return fmt.Errorf("PAGEGLEAN_WEBHOOK_URL must be an HTTP(S) URL without credentials or a fragment")
	}
	if len(secret) < 32 {
		return fmt.Errorf("PAGEGLEAN_WEBHOOK_SECRET must contain at least 32 bytes")
	}
	return nil
}

func ValidateWebhookToken(token string) error {
	for _, c := range []byte(token) {
		if c < 0x21 || c > 0x7e {
			return fmt.Errorf("PAGEGLEAN_WEBHOOK_TOKEN must contain only visible ASCII characters")
		}
	}
	return nil
}

// ValidateWebhookConfig keeps the existing generic protocol as the default.
func ValidateWebhookConfig(mode, endpoint, secret, token string) error {
	if err := ValidateWebhookToken(token); err != nil {
		return err
	}
	switch mode {
	case "", "generic":
		return ValidateWebhook(endpoint, secret)
	case "rivet":
		if endpoint == "" {
			return nil
		}
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.ForceQuery {
			return fmt.Errorf("PAGEGLEAN_WEBHOOK_URL must be an HTTP(S) URL without credentials, query, or fragment")
		}
		if token == "" {
			return fmt.Errorf("PAGEGLEAN_WEBHOOK_TOKEN is required in rivet mode")
		}
		return nil
	default:
		return fmt.Errorf("PAGEGLEAN_WEBHOOK_MODE must be generic or rivet")
	}
}
