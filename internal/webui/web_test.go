package webui

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
)

func TestAssetContentAndCaching(t *testing.T) {
	for _, name := range []string{"app.js", "styles.css", "reader.css", "favicon.svg"} {
		t.Run(name, func(t *testing.T) {
			data, err := Assets.ReadFile("assets/" + name)
			if err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(data))
			ext := path.Ext(name)
			wantURL := "/assets/" + strings.TrimSuffix(name, ext) + "." + hash + ext
			if got := AssetURL(name); got != wantURL {
				t.Fatalf("asset URL = %q, want %q", got, wantURL)
			}
			for _, url := range []string{wantURL, "/" + name} {
				cache := "no-cache"
				if url == wantURL {
					cache = "public, max-age=31536000, immutable"
				}
				w := httptest.NewRecorder()
				ServeAsset(w, httptest.NewRequest(http.MethodGet, url, nil))
				if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) {
					t.Fatalf("GET %s: status = %d, content matches = %v", url, w.Code, bytes.Equal(w.Body.Bytes(), data))
				}
				if w.Header().Get("Cache-Control") != cache || w.Header().Get("ETag") != `"`+hash+`"` || w.Header().Get("Content-Type") == "" {
					t.Fatalf("GET %s: headers = %v", url, w.Header())
				}
				for _, method := range []string{http.MethodGet, http.MethodHead} {
					for _, match := range []string{`"` + hash + `"`, `"other", W/"` + hash + `"`, "*"} {
						r := httptest.NewRequest(method, url, nil)
						r.Header.Set("If-None-Match", match)
						w := httptest.NewRecorder()
						ServeAsset(w, r)
						if w.Code != http.StatusNotModified || w.Body.Len() != 0 || w.Header().Get("Cache-Control") != cache || w.Header().Get("ETag") == "" {
							t.Fatalf("conditional %s %s: %d %v", method, url, w.Code, w.Header())
						}
					}
				}
				r := httptest.NewRequest(http.MethodGet, url, nil)
				r.Header.Set("If-None-Match", `"old-version"`)
				w = httptest.NewRecorder()
				ServeAsset(w, r)
				if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), data) {
					t.Fatalf("stale ETag: %d", w.Code)
				}
				w = httptest.NewRecorder()
				ServeAsset(w, httptest.NewRequest(http.MethodHead, url, nil))
				if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Length") != fmt.Sprint(len(data)) {
					t.Fatalf("HEAD %s: %d %v", url, w.Code, w.Header())
				}
			}
		})
	}
}

func TestMissingAssetsAreNotCached(t *testing.T) {
	for _, url := range []string{"/assets/app.old.js", strings.Replace(AssetURL("app.js"), "/assets/app.", "/assets/app.0", 1), "/missing.css", "/assets/index.html", "/index.html", "/assets/", "/assets/app.js"} {
		w := httptest.NewRecorder()
		ServeAsset(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != http.StatusNotFound || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("ETag") != "" {
			t.Fatalf("GET %s: %d %v", url, w.Code, w.Header())
		}
	}
}
