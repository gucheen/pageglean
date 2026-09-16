package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func TestIndexStartsInCorrectView(t *testing.T) {
	a, data := newTestApp(t)
	token, err := data.CreateAppSession(t.Context(), 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, token, view string }{
		{"/", "", "authView"}, {"/index.html", "", "authView"}, {"/add", "", "authView"},
		{"/", token, "appView"}, {"/index.html", token, "appView"}, {"/add", token, "captureView"}, {"/", "expired", "authView"},
	} {
		r := httptest.NewRequest(http.MethodGet, tc.path, nil)
		if tc.token != "" {
			r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: tc.token})
		}
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("response %d %v", w.Code, w.Header())
		}
		if strings.Contains(w.Body.String(), "loadingView") || strings.Contains(w.Body.String(), "{{") {
			t.Fatal("unrendered or loading UI")
		}
		z := html.NewTokenizer(strings.NewReader(w.Body.String()))
		found := false
		for {
			typ := z.Next()
			if typ == html.ErrorToken {
				break
			}
			if typ != html.StartTagToken {
				continue
			}
			tok := z.Token()
			id := ""
			hidden := false
			for _, attr := range tok.Attr {
				if attr.Key == "id" {
					id = attr.Val
				}
				if attr.Key == "hidden" {
					hidden = true
				}
			}
			if id == "authView" || id == "appView" || id == "captureView" {
				if hidden == (id == tc.view) {
					t.Fatalf("%s: %s hidden=%v", tc.path, id, hidden)
				}
				if id == tc.view {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("missing initial view")
		}
	}
}
