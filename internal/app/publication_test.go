package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pageglean/internal/importer"
	"pageglean/internal/store"
)

type publicationTransport func(*http.Request) (*http.Response, error)

func (f publicationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicationAPISeparatesPublicAndPrivateFields(t *testing.T) {
	a, data := newTestApp(t)
	token, err := data.CreateAppSession(t.Context(), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}
	body := `{"url":"https://example.com","title":"Public title","description":"Page description","note":"PRIVATE_NOTE","publicComment":"Recommendation","public":true,"archive":false}`
	created := call(http.MethodPost, "/api/bookmarks", body, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	feed := call(http.MethodGet, "/public/bookmarks.json", "", false)
	if feed.Code != http.StatusOK || strings.Contains(feed.Body.String(), "PRIVATE_NOTE") || !strings.Contains(feed.Body.String(), "Recommendation") || !strings.Contains(feed.Body.String(), "Page description") {
		t.Fatalf("feed: %s", feed.Body.String())
	}
	r := httptest.NewRequest(http.MethodGet, "/public/bookmarks.json", nil)
	r.Header.Set("If-None-Match", `"other", W/`+feed.Header().Get("ETag"))
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
		t.Fatalf("conditional: %d %s", w.Code, w.Body.String())
	}
	for _, path := range []string{"/api/bookmarks", "/api/publication", "/api/export", "/archive/1"} {
		if w := call(http.MethodGet, path, "", false); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if w := call(http.MethodPost, "/api/publication/retry", "", false); w.Code != http.StatusUnauthorized {
		t.Fatal("retry requires auth")
	}
	if w := call(http.MethodPatch, "/api/bookmarks/1", `{"public":false,"note":"still private"}`, true); w.Code != http.StatusOK {
		t.Fatalf("update: %s", w.Body.String())
	}
	empty := call(http.MethodGet, "/public/bookmarks.json", "", false)
	if !strings.Contains(empty.Body.String(), `"bookmarks":[]`) {
		t.Fatalf("empty feed: %s", empty.Body.String())
	}
	if w := call(http.MethodPost, "/api/publication/retry", "", true); w.Code != http.StatusConflict {
		t.Fatal("unconfigured retry should fail")
	}
}

func TestWebhookSigningRetryAndNoRestartReplay(t *testing.T) {
	a, data := newTestApp(t)
	a.cfg.WebhookURL = "https://hooks.example/notify?token=SECRET_URL"
	a.cfg.WebhookToken = "receiver-token"
	a.cfg.WebhookSecret = strings.Repeat("s", 32)
	var events []string
	fail := true
	a.webhookClient.Transport = publicationTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer receiver-token" {
			t.Fatal("missing webhook authentication token")
		}
		if _, present := r.Header["X-Webhook-Token"]; present {
			t.Fatal("unexpected X-Webhook-Token header")
		}
		body, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(a.cfg.WebhookSecret))
		mac.Write([]byte(r.Header.Get("X-PageGlean-Timestamp") + "."))
		mac.Write(body)
		if r.Header.Get("X-PageGlean-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Fatal("signature mismatch")
		}
		var event map[string]any
		if err := json.Unmarshal(body, &event); err != nil {
			t.Fatal(err)
		}
		if event["event"] != "public_bookmarks.changed" || event["feedUrl"] != "http://localhost:8080/public/bookmarks.json" || event["eventId"] != r.Header.Get("X-PageGlean-Event-ID") {
			t.Fatalf("event: %#v", event)
		}
		if _, exists := event["bookmarks"]; exists {
			t.Fatal("webhook should carry notification only")
		}
		events = append(events, event["eventId"].(string))
		if fail {
			return nil, errors.New("transport secret SECRET_URL")
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	if err := a.processPublication(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 || a.publicationStatus().DueAt != "" {
		t.Fatal("startup must not notify")
	}
	_, _, err := data.CreateBookmark(t.Context(), store.Bookmark{URL: "https://example.com", CanonicalURL: "https://example.com", Public: true, SkipArchive: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.processPublication(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 || a.publicationStatus().DueAt == "" {
		t.Fatal("changes must debounce")
	}
	for range 5 {
		a.publicationMu.Lock()
		a.publication.DueAt = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
		a.publicationMu.Unlock()
		if err := a.processPublication(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	status := a.publicationStatus()
	if status.Attempts != 5 || status.DueAt != "" || strings.Contains(status.LastError, "SECRET_URL") {
		t.Fatalf("retry state: %#v", status)
	}
	for _, id := range events {
		if id != events[0] {
			t.Fatal("retries must retain event ID")
		}
	}
	if err := a.processPublication(t.Context()); err != nil || len(events) != 5 {
		t.Fatal("exhausted retry continued")
	}
	fail = false
	if err := a.refreshPublication(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	if err := a.processPublication(t.Context()); err != nil {
		t.Fatal(err)
	}
	if a.publicationStatus().NotifiedAt == "" || a.publicationStatus().LastError != "" {
		t.Fatal("retry did not succeed")
	}
	if events[5] == events[0] {
		t.Fatal("manual retry should have a new event ID")
	}
	restarted, err := New(a.cfg, data, a.logger)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.publicationStatus().NotifiedAt != "" {
		t.Fatal("notification history should be in memory")
	}
	if err := restarted.refreshPublication(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if restarted.publicationStatus().DueAt != "" {
		t.Fatal("restart must not resend")
	}
	if err := restarted.webhookClient.CheckRedirect(nil, nil); err != http.ErrUseLastResponse {
		t.Fatal("webhooks must not follow redirects")
	}
}

func TestNotificationCoalescingAndChangeDuringDelivery(t *testing.T) {
	a, data := newTestApp(t)
	a.cfg.WebhookURL = "https://hooks.example/notify"
	a.cfg.WebhookSecret = strings.Repeat("s", 32)
	b, _, err := data.CreateBookmark(t.Context(), store.Bookmark{URL: "https://example.com", CanonicalURL: "https://example.com", Public: true, Title: "first", SkipArchive: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.refreshPublication(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	first := a.publicationStatus()
	b.Note = "private edit"
	if _, err := data.UpdateBookmark(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	a.refreshPublication(t.Context(), false)
	if a.publicationStatus().Generation != first.Generation {
		t.Fatal("private edit enqueued notification")
	}
	b.PublicComment = "new recommendation"
	data.UpdateBookmark(t.Context(), b)
	a.refreshPublication(t.Context(), false)
	if a.publicationStatus().Generation != first.Generation+1 {
		t.Fatal("public edit did not replace pending notification")
	}
	a.refreshPublication(t.Context(), true)
	a.webhookClient.Transport = publicationTransport(func(r *http.Request) (*http.Response, error) {
		if _, present := r.Header["Authorization"]; present {
			t.Fatal("unconfigured token header must be omitted")
		}
		b.Public = false
		if _, err := data.UpdateBookmark(t.Context(), b); err != nil {
			t.Fatal(err)
		}
		if err := a.refreshPublication(t.Context(), false); err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})
	if err := a.processPublication(t.Context()); err != nil {
		t.Fatal(err)
	}
	status := a.publicationStatus()
	feed, _ := data.PublicFeed(t.Context())
	if status.DueAt == "" || status.Revision != feed.Revision || status.NotifiedAt != "" {
		t.Fatalf("new change lost: %#v", status)
	}
}

func TestImportedPublicFieldsRemainPrivate(t *testing.T) {
	items, invalid := prepareImportedItems([]importer.Item{{URL: "https://example.com", Title: "title", Description: "source description", Note: "private note", PublicComment: "public recommendation"}}, true)
	if invalid != 0 || len(items) != 1 || items[0].Public || items[0].Description != "source description" || items[0].PublicComment != "public recommendation" || items[0].Note != "private note" {
		t.Fatalf("import: %#v", items)
	}
}

func TestRivetDeliveryProtocolAndRetry(t *testing.T) {
	a, data := newTestApp(t)
	a.cfg.WebhookMode = "rivet"
	a.cfg.WebhookURL = "https://hooks.example/api/v1/repos/blog/triggers/rebuild"
	a.cfg.WebhookToken = "rivet-token"
	if _, err := New(a.cfg, data, a.logger); err != nil {
		t.Fatal(err)
	}
	code := 503
	networkFailure := false
	var keys []string
	a.webhookClient.Transport = publicationTransport(func(r *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "{}" || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("invalid Rivet request: %s, %v", body, err)
		}
		if r.URL.Path != "/api/v1/repos/blog/triggers/rebuild" || r.Header.Get("Authorization") != "Bearer rivet-token" {
			t.Fatal("wrong target or authentication")
		}
		for _, header := range []string{"X-PageGlean-Signature", "X-PageGlean-Timestamp", "X-PageGlean-Event-ID"} {
			if r.Header.Get(header) != "" {
				t.Fatal("unexpected legacy header")
			}
		}
		key := r.Header.Get("Idempotency-Key")
		if len(key) < 1 || len(key) > 128 {
			t.Fatal("invalid key length")
		}
		for _, c := range key {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
				t.Fatal("invalid key character")
			}
		}
		keys = append(keys, key)
		if networkFailure {
			return nil, errors.New("secret transport detail")
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})
	send := func(manual bool) {
		t.Helper()
		if manual {
			if err := a.refreshPublication(t.Context(), true); err != nil {
				t.Fatal(err)
			}
		} else {
			a.publicationMu.Lock()
			a.publication.DueAt = time.Now().Add(-time.Second).Format(time.RFC3339Nano)
			a.publicationMu.Unlock()
		}
		if err := a.processPublication(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	send(true)
	if a.publicationStatus().DueAt == "" {
		t.Fatal("503 must retry")
	}
	networkFailure = true
	send(false)
	if a.publicationStatus().DueAt == "" || strings.Contains(a.publicationStatus().LastError, "secret") {
		t.Fatal("network retry or redaction failed")
	}
	networkFailure = false
	code = 201
	send(true)
	if keys[0] != keys[1] || keys[0] != keys[2] {
		t.Fatal("automatic and manual retries must retain key")
	}
	if a.publicationStatus().NotifiedAt == "" || a.publicationStatus().DueAt != "" {
		t.Fatal("201 must succeed")
	}
	code = 200
	send(true)
	if keys[3] == keys[2] {
		t.Fatal("explicit send after success must create new delivery")
	}
	if a.publicationStatus().LastError != "" || a.publicationStatus().DueAt != "" {
		t.Fatal("200 must succeed")
	}
	for _, statusCode := range []int{400, 401, 413, 415, 422, 204, 302, 500} {
		code = statusCode
		send(true)
		status := a.publicationStatus()
		if status.LastError == "" || status.DueAt != "" {
			t.Fatalf("HTTP %d must stop automatic retries", code)
		}
	}
	failedKey := keys[len(keys)-1]
	code = 201
	send(true)
	if keys[len(keys)-1] != failedKey {
		t.Fatal("retry after configuration repair must retain key")
	}
	if err := a.refreshPublication(t.Context(), true); err != nil {
		t.Fatal(err)
	}
	pending := a.publicationStatus()
	_, _, err := data.CreateBookmark(t.Context(), store.Bookmark{URL: "https://example.com/new", CanonicalURL: "https://example.com/new", Public: true, SkipArchive: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.refreshPublication(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	newer := a.publicationStatus()
	if newer.EventID == pending.EventID {
		t.Fatal("new content must create new key")
	}
	a.finishPublication(pending, "", true)
	if a.publicationStatus().DueAt == "" {
		t.Fatal("old delivery cleared newer change")
	}
}

func TestWebhookFailureLogs(t *testing.T) {
	for _, tc := range []struct {
		name, mode                string
		code                      int
		transportError, retryable bool
	}{
		{"network", "rivet", 0, true, true},
		{"unprocessable", "rivet", 422, false, false},
		{"unavailable", "rivet", 503, false, true},
		{"generic", "", 500, false, true},
		{"success", "rivet", 201, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := newTestApp(t)
			a.cfg.WebhookMode = tc.mode
			a.cfg.WebhookURL = "https://hooks.example/SECRET_PATH?token=SECRET_QUERY"
			a.cfg.WebhookToken = "SECRET_TOKEN"
			a.cfg.WebhookSecret = "SECRET_SIGNING_KEY"
			var logs bytes.Buffer
			a.logger = slog.New(slog.NewJSONHandler(&logs, nil))
			a.webhookClient.Transport = publicationTransport(func(r *http.Request) (*http.Response, error) {
				if tc.transportError {
					return nil, errors.New("SECRET_TRANSPORT " + r.URL.String())
				}
				return &http.Response{StatusCode: tc.code, Body: io.NopCloser(strings.NewReader("pipeline unavailable: " + a.cfg.WebhookToken + " " + a.cfg.WebhookSecret + " " + a.publicationStatus().EventID)), Header: make(http.Header)}, nil
			})
			if err := a.refreshPublication(t.Context(), true); err != nil {
				t.Fatal(err)
			}
			eventID := a.publicationStatus().EventID
			if err := a.processPublication(t.Context()); err != nil {
				t.Fatal(err)
			}
			if tc.code == 201 {
				if logs.Len() != 0 {
					t.Fatal("successful delivery must not log an error")
				}
				return
			}
			var entry map[string]any
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			mode := tc.mode
			if mode == "" {
				mode = "generic"
			}
			if entry["level"] != "ERROR" || entry["msg"] != "webhook request failed" || entry["mode"] != mode || entry["attempt"] != float64(1) || entry["status_code"] != float64(tc.code) || entry["retryable"] != tc.retryable || entry["error"] == "" {
				t.Fatalf("unexpected log: %s", logs.String())
			}
			if !tc.transportError && entry["response_body"] != "pipeline unavailable: [REDACTED] [REDACTED] [REDACTED]" {
				t.Fatalf("missing response diagnostics: %s", logs.String())
			}
			if strings.Contains(logs.String(), "SECRET_") || strings.Contains(logs.String(), eventID) {
				t.Fatal("webhook log exposed sensitive values")
			}
		})
	}
}

func TestWebhookResponseExcerptLimit(t *testing.T) {
	body, truncated := webhookResponseExcerpt([]byte(strings.Repeat("x", 5000)), "token")
	if len(body) != 4096 || !truncated {
		t.Fatal("response must be bounded")
	}
	body, truncated = webhookResponseExcerpt([]byte("invalid pipeline"), "token", "")
	if body != "invalid pipeline" || truncated {
		t.Fatal("short response changed")
	}
	body, truncated = webhookResponseExcerpt([]byte(strings.Repeat("x", 4085)+"SECRET_TOKEN"), "SECRET_TOKEN_LONGER")
	if strings.Contains(body, "SECRET_") || !truncated {
		t.Fatal("partial credential leaked at read boundary")
	}
}
