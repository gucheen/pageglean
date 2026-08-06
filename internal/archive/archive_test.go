package archive

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"links/internal/config"
	"links/internal/store"
)

func TestProcessOneArchivesReadableText(t *testing.T) {
	htmlBody := `<!doctype html><html><head><title>测试文章</title></head><body>
		<nav>Navigation noise</nav><article><h1>测试文章</h1><p>这是用于验证正文归档的一段足够长的中文内容。</p>
		<p>第二段内容包含 &lt;unsafe&gt; 字样，但归档页面必须保持安全。</p></article></body></html>`

	dataDir := t.TempDir()
	data, err := store.Open(filepath.Join(dataDir, "links.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer data.Close()
	bookmark, _, err := data.CreateBookmark(context.Background(), store.Bookmark{
		URL: "https://example.com/article", CanonicalURL: "https://example.com/article", Title: "",
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := New(config.Config{DataDir: dataDir, AllowPrivateFetch: true}, data, slog.New(slog.NewTextHandler(io.Discard, nil)))
	worker.client = &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
			Body:       io.NopCloser(strings.NewReader(htmlBody)),
		}, nil
	})}
	processed, err := worker.ProcessOne(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !processed {
		t.Fatal("expected one archive job to be processed")
	}
	updated, err := data.GetBookmark(context.Background(), bookmark.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ArchiveStatus != "complete" || updated.ContentPath == "" || updated.ContentHash == "" {
		t.Fatalf("unexpected archive state: %#v", updated)
	}
	fragment, err := worker.Read(updated.ContentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(fragment), "正文归档") || strings.Contains(string(fragment), "<script") {
		t.Fatalf("unexpected archive fragment: %s", fragment)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestPrivateAddressesAreBlocked(t *testing.T) {
	for _, value := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "::1"} {
		if isPublicIP(net.ParseIP(value)) {
			t.Fatalf("%s was considered public", value)
		}
	}
	if !isPublicIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("public address was blocked")
	}
}
