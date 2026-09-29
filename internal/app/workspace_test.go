package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pageglean/internal/importer"
	"pageglean/internal/store"
)

func TestWorkspaceAPIAndExportRoundTrip(t *testing.T) {
	a, data := newTestApp(t)
	token, err := data.CreateAppSession(t.Context(), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		return w
	}
	w := call("POST", "/api/bookmarks", `{"url":"https://example.com/home","title":"Original","home":true,"homeTitle":"Shortcut","homeGroup":"Work","homeOrder":12,"homePinned":true,"public":true}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var created struct {
		Bookmark store.Bookmark `json:"bookmark"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Bookmark.Home || created.Bookmark.Library || created.Bookmark.ArchiveStatus != "idle" {
		t.Fatalf("created: %+v", created)
	}
	for _, format := range []string{"json", "csv"} {
		w = call("GET", "/api/export?format="+format, "")
		if w.Code != http.StatusOK {
			t.Fatalf("export: %s", w.Body)
		}
		parsed, err := importer.Parse("bookmarks."+format, bytes.NewReader(w.Body.Bytes()), importer.Mapping{})
		if err != nil {
			t.Fatal(err)
		}
		items, invalid := prepareImportedItems(parsed.Items, true)
		if invalid != 0 || len(items) != 1 {
			t.Fatalf("roundtrip: %+v", parsed)
		}
		item := items[0]
		if !item.Home || item.Library || item.HomeTitle != "Shortcut" || item.HomeGroup != "Work" || item.HomeOrder != 12 || !item.HomePinned || item.Public {
			t.Fatalf("%s roundtrip: %+v", format, item)
		}
	}
	w = call("PATCH", fmt.Sprintf("/api/bookmarks/%d", created.Bookmark.ID), `{"home":false}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("orphan: %d %s", w.Code, w.Body)
	}
	w = call("PATCH", fmt.Sprintf("/api/bookmarks/%d", created.Bookmark.ID), `{"home":false,"library":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	w = call("GET", "/api/bookmarks?scope=home", "")
	if !strings.Contains(w.Body.String(), `"bookmarks":[]`) {
		t.Fatalf("home: %s", w.Body)
	}
	w = call("GET", "/api/bookmarks?scope=library&q=Shortcut", "")
	if !strings.Contains(w.Body.String(), `"homeTitle":"Shortcut"`) {
		t.Fatalf("library search: %s", w.Body)
	}
	w = call("GET", "/public/bookmarks.json", "")
	if strings.Contains(w.Body.String(), "Shortcut") || strings.Contains(w.Body.String(), "homeGroup") {
		t.Fatalf("private home metadata leaked: %s", w.Body)
	}
}

func TestImportBrowserBookmarksToHome(t *testing.T) {
	a, data := newTestApp(t)
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
	io.WriteString(file, `<!DOCTYPE NETSCAPE-Bookmark-file-1><DL><p><DT><A HREF="https://example.com">Browser link</A></DL><p>`)
	writer.WriteField("destination", "home")
	writer.Close()
	r := httptest.NewRequest("POST", "/api/import", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("import: %s", w.Body)
	}
	items, err := data.ListBookmarks(t.Context(), store.BookmarkFilter{Scope: "home"})
	if err != nil || len(items) != 1 || items[0].Library || items[0].ArchiveStatus != "idle" {
		t.Fatalf("home import: %+v, %v", items, err)
	}
}
