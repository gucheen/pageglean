package app

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pageglean/internal/store"
	"pageglean/internal/webui"
)

func TestPagesReferenceVersionedAssets(t *testing.T) {
	a, _ := newTestApp(t)
	for _, url := range []string{"/", "/index.html", "/library", "/all", "/add", "/public/"} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s: %d %v", url, w.Code, w.Header())
		}
		names := []string{"styles.css", "favicon.svg"}
		if url != "/public/" {
			names = append(names, "app.js")
		}
		for _, name := range names {
			if !strings.Contains(w.Body.String(), `"`+webui.AssetURL(name)+`"`) {
				t.Fatalf("GET %s: missing versioned %s", url, name)
			}
		}
	}
}

func TestAssetRoutes(t *testing.T) {
	a, _ := newTestApp(t)
	for _, tc := range []struct {
		url    string
		status int
		cache  string
	}{
		{webui.AssetURL("app.js"), http.StatusOK, "public, max-age=31536000, immutable"},
		{"/app.js", http.StatusOK, "no-cache"},
		{"/assets/unknown", http.StatusNotFound, "no-store"},
		{"/assets/app.old.js", http.StatusNotFound, "no-store"},
	} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if w.Code != tc.status || w.Header().Get("Cache-Control") != tc.cache || w.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("GET %s: %d %v", tc.url, w.Code, w.Header())
		}
	}
}

func TestArchiveReferencesVersionedStylesheet(t *testing.T) {
	a, data := newTestApp(t)
	bookmark, _, err := data.CreateBookmark(t.Context(), store.Bookmark{
		URL: "https://example.com/article", CanonicalURL: "https://example.com/article", Title: "Article", SkipArchive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := gzip.NewWriter(&body)
	if _, err := writer.Write([]byte("<article>Archived content</article>")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.cfg.DataDir, "article.html.gz"), body.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := data.CompleteArchive(t.Context(), bookmark.ID, store.ArchiveContent{Path: "article.html.gz", Hash: "test", Text: "Archived content"}); err != nil {
		t.Fatal(err)
	}
	token, err := data.CreateAppSession(t.Context(), 1, time.Hour, a.authIdentity())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/archive/%d", bookmark.ID), nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "private, max-age=3600" || !strings.Contains(w.Body.String(), `href="`+webui.AssetURL("reader.css")+`"`) || !strings.Contains(w.Body.String(), "Archived content") {
		t.Fatalf("archive response: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
}
