package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
		if r.Header.Get("X-Webhook-Token") != "receiver-token" {
			t.Fatal("missing webhook authentication token")
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
		if _, present := r.Header["X-Webhook-Token"]; present {
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
