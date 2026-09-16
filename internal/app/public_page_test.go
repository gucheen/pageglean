package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pageglean/internal/store"
)

func TestPublicPagePrivacyPaginationAndWithdrawal(t *testing.T) {
	a, data := newTestApp(t)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	if w := get("/public/"); w.Code != 200 || !strings.Contains(w.Body.String(), "还没有公开书签") {
		t.Fatal("missing empty state")
	}
	var newest store.Bookmark
	for i := range 31 {
		u := fmt.Sprintf("https://example.com/%d", i)
		b, _, err := data.CreateBookmark(t.Context(), store.Bookmark{URL: u, CanonicalURL: u, Title: fmt.Sprintf("Bookmark-%02d", i), Public: true, Description: "网页描述", PublicComment: "<script>alert(1)</script>公开短评", Note: "PRIVATE_NOTE", Tags: []string{"PRIVATE_TAG"}, SkipArchive: true, CreatedAt: time.Date(2026, 1, 1, 0, i, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		newest = b
	}
	if _, _, err := data.CreateBookmark(t.Context(), store.Bookmark{URL: "https://private.example", CanonicalURL: "https://private.example", Title: "PRIVATE_TITLE", PublicComment: "PRIVATE_COMMENT", SkipArchive: true}); err != nil {
		t.Fatal(err)
	}
	w := get("/public/")
	body := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response: %d %v", w.Code, w.Header())
	}
	for _, unwanted := range []string{"PRIVATE", "Bookmark-00", "<script>", "/archive/", "app.js"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("unexpected %s", unwanted)
		}
	}
	if strings.Count(body, "<article") != 30 || !strings.Contains(body, "/public/?page=2") || !strings.Contains(body, "&lt;script&gt;") || !strings.Contains(body, "网页描述") {
		t.Fatal("missing page content")
	}
	if strings.Index(body, "Bookmark-30") > strings.Index(body, "Bookmark-29") {
		t.Fatal("wrong order")
	}
	second := get("/public/?page=2")
	if second.Code != 200 || strings.Count(second.Body.String(), "<article") != 1 || !strings.Contains(second.Body.String(), "Bookmark-00") || !strings.Contains(second.Body.String(), "上一页") || strings.Contains(second.Body.String(), "下一页") {
		t.Fatal("incorrect second page")
	}
	for _, page := range []string{"0", "-1", "oops", "2147483648"} {
		if get("/public/?page="+page).Code != 400 {
			t.Fatalf("accepted page %s", page)
		}
	}
	if get("/public/?page=3").Code != 404 {
		t.Fatal("missing page must return 404")
	}
	newest.Public = false
	if _, err := data.UpdateBookmark(t.Context(), newest); err != nil {
		t.Fatal(err)
	}
	withdrawn := get("/public/").Body.String()
	if strings.Contains(withdrawn, "Bookmark-30") || !strings.Contains(withdrawn, "Bookmark-00") || strings.Contains(withdrawn, "下一页") {
		t.Fatal("withdrawal not reflected")
	}
	feed, err := data.PublicFeed(t.Context())
	if err != nil || len(feed.Bookmarks) != 6 {
		t.Fatal("latest-six feed changed")
	}
}
