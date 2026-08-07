package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pageglean/internal/config"
	"pageglean/internal/store"
)

func newTestApp(t *testing.T) (*App, *store.Store) {
	t.Helper()
	data, err := store.Open(filepath.Join(t.TempDir(), "pageglean.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = data.Close() })
	cfg := config.Config{
		PublicURL: "http://localhost:8080", PublicOrigin: "http://localhost:8080",
		RPID: "localhost", SecureCookies: false, DataDir: t.TempDir(),
	}
	application, err := New(cfg, data, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return application, data
}

func TestCaptureAcceptsPairedExtensionToken(t *testing.T) {
	application, data := newTestApp(t)
	code, _, err := data.CreateExtensionPairing(t.Context(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	token, err := data.RedeemExtensionPairing(t.Context(), code, "Test Chromium")
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(`{"url":"https://example.com/article","title":"Extension capture"}`)
	request := httptest.NewRequest(http.MethodPost, "/api/capture", body)
	request.Header.Set("Origin", "chrome-extension://abcdefghijklmnop")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "chrome-extension://abcdefghijklmnop" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestExtensionPreflightIsLimitedToCaptureRoutes(t *testing.T) {
	application, _ := newTestApp(t)
	request := httptest.NewRequest(http.MethodOptions, "/api/capture", nil)
	request.Header.Set("Origin", "chrome-extension://abcdefghijklmnop")
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestStatusReportsSetupRequired(t *testing.T) {
	application, _ := newTestApp(t)
	request := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var payload struct {
		SetupRequired bool `json:"setupRequired"`
		Authenticated bool `json:"authenticated"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if !payload.SetupRequired || payload.Authenticated {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestRegistrationStartCreatesUnexpiredCeremony(t *testing.T) {
	application, data := newTestApp(t)
	setupToken, err := data.CreateAdminToken(t.Context(), "setup", 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"token": setupToken, "label": "Passkey"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/auth/register/start", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var ceremonyCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == ceremonyCookieName {
			ceremonyCookie = cookie
			break
		}
	}
	if ceremonyCookie == nil {
		t.Fatal("registration start did not set the ceremony cookie")
	}
	if ceremonyCookie.MaxAge <= 0 || !ceremonyCookie.Expires.After(time.Now()) {
		t.Fatalf("ceremony cookie is already expired: %#v", ceremonyCookie)
	}
	session, _, err := data.TakeCeremony(t.Context(), ceremonyCookie.Value, "registration")
	if err != nil {
		t.Fatalf("stored ceremony is already expired: %v", err)
	}
	if !session.Expires.After(time.Now()) {
		t.Fatalf("stored ceremony expiry = %s", session.Expires)
	}
}

func TestBookmarksRequireAuthentication(t *testing.T) {
	application, _ := newTestApp(t)
	request := httptest.NewRequest(http.MethodGet, "/api/bookmarks", nil)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestOriginCheckRejectsCrossSiteMutation(t *testing.T) {
	application, _ := newTestApp(t)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login/start", nil)
	request.Header.Set("Origin", "https://evil.example")
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
}

func TestStaticAppHasSecurityHeaders(t *testing.T) {
	application, _ := newTestApp(t)
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing Content-Security-Policy")
	}
}

func TestAuthenticatedExport(t *testing.T) {
	application, data := newTestApp(t)
	_, _, err := data.CreateBookmark(t.Context(), store.Bookmark{
		URL: "https://example.com", CanonicalURL: "https://example.com/", Title: "Export me",
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := data.CreateAppSession(t.Context(), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/export?format=json", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Export me") {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestAuthenticatedHTMLImportSkipsArchiveByDefault(t *testing.T) {
	application, data := newTestApp(t)
	token, err := data.CreateAppSession(t.Context(), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "bookmarks.html")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(file, `<!DOCTYPE NETSCAPE-Bookmark-file-1><DL><p><DT><A HREF="https://example.com/imported" TAGS="迁移">导入文章</A></DL><p>`)
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	items, err := data.ListBookmarks(t.Context(), store.BookmarkFilter{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ArchiveStatus != "idle" || items[0].CaptureSource != "import" {
		t.Fatalf("unexpected imported bookmark: %#v", items)
	}
}

func TestAuthenticatedBulkUpdate(t *testing.T) {
	application, data := newTestApp(t)
	bookmark, _, err := data.CreateBookmark(t.Context(), store.Bookmark{
		URL: "https://example.com/bulk", CanonicalURL: "https://example.com/bulk",
	})
	if err != nil {
		t.Fatal(err)
	}
	token, err := data.CreateAppSession(t.Context(), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.NewBufferString(fmt.Sprintf(`{"ids":[%d],"addTags":["批量"],"unread":true}`, bookmark.ID))
	request := httptest.NewRequest(http.MethodPatch, "/api/bookmarks/bulk", body)
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	response := httptest.NewRecorder()
	application.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := data.GetBookmark(t.Context(), bookmark.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Unread || len(updated.Tags) != 1 || updated.Tags[0] != "批量" {
		t.Fatalf("unexpected bulk update: %#v", updated)
	}
}
