package archive

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	readability "github.com/go-shiori/go-readability"

	"pageglean/internal/config"
	"pageglean/internal/store"
)

const (
	maxResponseBytes = 10 << 20
	maxTextBytes     = 2 << 20
)

type Archiver struct {
	store   *store.Store
	dataDir string
	client  *http.Client
	logger  *slog.Logger
}

func New(cfg config.Config, data *store.Store, logger *slog.Logger) *Archiver {
	if logger == nil {
		logger = slog.Default()
	}
	return &Archiver{
		store: data, dataDir: cfg.DataDir,
		client: newHTTPClient(cfg.AllowPrivateFetch), logger: logger,
	}
}

func (a *Archiver) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		processed, err := a.ProcessOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			a.logger.Error("archive worker failed", "error", err)
		}
		if processed {
			timer.Reset(100 * time.Millisecond)
		} else {
			timer.Reset(2 * time.Second)
		}
	}
}

func (a *Archiver) ProcessOne(ctx context.Context) (bool, error) {
	job, err := a.store.ClaimArchiveJob(ctx)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	content, err := a.fetchAndStore(ctx, job.URL)
	if err != nil {
		if storeErr := a.store.FailArchive(ctx, job.BookmarkID, err.Error()); storeErr != nil {
			return true, fmt.Errorf("archive %d failed: %v; save failure: %w", job.BookmarkID, err, storeErr)
		}
		a.logger.Warn("bookmark archive attempt failed", "bookmark_id", job.BookmarkID, "attempt", job.Attempts+1, "error", err)
		return true, nil
	}
	if err := a.store.CompleteArchive(ctx, job.BookmarkID, content); err != nil {
		return true, err
	}
	a.logger.Info("bookmark archived", "bookmark_id", job.BookmarkID, "content_hash", content.Hash)
	return true, nil
}

func (a *Archiver) StoreClientText(ctx context.Context, bookmarkID int64, title, value string) error {
	text := truncateUTF8(normalizeText(value), maxTextBytes)
	if text == "" {
		return fmt.Errorf("no readable text supplied")
	}
	fragment := buildSafeFragment(title, "", text)
	hashBytes := sha256.Sum256([]byte(fragment))
	hash := hex.EncodeToString(hashBytes[:])
	relative := filepath.Join("blobs", hash[:2], hash+".html.gz")
	if err := a.writeCompressed(relative, []byte(fragment)); err != nil {
		return err
	}
	return a.store.CompleteArchive(ctx, bookmarkID, store.ArchiveContent{
		Title: strings.TrimSpace(title), Text: text,
		Path: filepath.ToSlash(relative), Hash: hash,
	})
}

func (a *Archiver) fetchAndStore(ctx context.Context, value string) (store.ArchiveContent, error) {
	pageURL, err := url.Parse(value)
	if err != nil {
		return store.ArchiveContent{}, fmt.Errorf("parse URL: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL.String(), nil)
	if err != nil {
		return store.ArchiveContent{}, err
	}
	request.Header.Set("User-Agent", "PageGlean/1.0 (+personal bookmark archiver)")
	request.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9")

	response, err := a.client.Do(request)
	if err != nil {
		return store.ArchiveContent{}, fmt.Errorf("fetch page: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return store.ArchiveContent{}, fmt.Errorf("page returned HTTP %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return store.ArchiveContent{}, fmt.Errorf("invalid Content-Type")
		}
		if mediaType != "text/html" && mediaType != "application/xhtml+xml" {
			return store.ArchiveContent{}, fmt.Errorf("unsupported Content-Type %s", mediaType)
		}
	}

	limited := io.LimitReader(response.Body, maxResponseBytes+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return store.ArchiveContent{}, fmt.Errorf("read page: %w", err)
	}
	if len(raw) > maxResponseBytes {
		return store.ArchiveContent{}, fmt.Errorf("page exceeds 10 MB response limit")
	}
	article, err := readability.FromReader(bytes.NewReader(raw), pageURL)
	if err != nil {
		return store.ArchiveContent{}, fmt.Errorf("extract readable content: %w", err)
	}
	text := normalizeText(article.TextContent)
	if text == "" {
		return store.ArchiveContent{}, fmt.Errorf("no readable text found")
	}
	text = truncateUTF8(text, maxTextBytes)
	fragment := buildSafeFragment(article.Title, article.Byline, text)
	hashBytes := sha256.Sum256([]byte(fragment))
	hash := hex.EncodeToString(hashBytes[:])
	relative := filepath.Join("blobs", hash[:2], hash+".html.gz")
	if err := a.writeCompressed(relative, []byte(fragment)); err != nil {
		return store.ArchiveContent{}, err
	}
	return store.ArchiveContent{
		Title: strings.TrimSpace(article.Title), Description: strings.TrimSpace(article.Excerpt),
		Author: strings.TrimSpace(article.Byline), Text: text,
		Path: filepath.ToSlash(relative), Hash: hash,
	}, nil
}

func (a *Archiver) writeCompressed(relative string, content []byte) error {
	target := filepath.Join(a.dataDir, filepath.FromSlash(relative))
	if info, err := os.Stat(target); err == nil && info.Mode().IsRegular() {
		return nil
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create archive directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".archive-*.tmp")
	if err != nil {
		return fmt.Errorf("create archive file: %w", err)
	}
	tempName := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o640); err != nil {
		return err
	}
	writer := gzip.NewWriter(temp)
	if _, err := writer.Write(content); err != nil {
		return fmt.Errorf("compress archive: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish archive compression: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempName, target); err != nil {
		return fmt.Errorf("commit archive: %w", err)
	}
	committed = true
	return nil
}

func (a *Archiver) Read(relative string) ([]byte, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if relative == "" || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("invalid archive path")
	}
	file, err := os.Open(filepath.Join(a.dataDir, clean))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, maxTextBytes+512*1024))
}

func buildSafeFragment(title, byline, text string) string {
	var builder strings.Builder
	builder.WriteString(`<article class="reader-article">`)
	if title = strings.TrimSpace(title); title != "" {
		builder.WriteString("<h1>")
		builder.WriteString(html.EscapeString(title))
		builder.WriteString("</h1>")
	}
	if byline = strings.TrimSpace(byline); byline != "" {
		builder.WriteString(`<p class="reader-byline">`)
		builder.WriteString(html.EscapeString(byline))
		builder.WriteString("</p>")
	}
	for _, paragraph := range strings.Split(text, "\n\n") {
		paragraph = strings.TrimSpace(paragraph)
		if paragraph == "" {
			continue
		}
		builder.WriteString("<p>")
		builder.WriteString(strings.ReplaceAll(html.EscapeString(paragraph), "\n", "<br>"))
		builder.WriteString("</p>")
	}
	builder.WriteString("</article>")
	return builder.String()
}

func normalizeText(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	result := make([]string, 0, len(lines))
	empty := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if !empty && len(result) > 0 {
				result = append(result, "")
			}
			empty = true
			continue
		}
		empty = false
		result = append(result, line)
	}
	return strings.TrimSpace(strings.Join(result, "\n"))
}

func truncateUTF8(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func newHTTPClient(allowPrivate bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 10 * time.Second
	transport.MaxIdleConnsPerHost = 2
	if !allowPrivate {
		transport.DialContext = safeDialContext
	}
	return &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			if request.URL.Scheme != "http" && request.URL.Scheme != "https" {
				return fmt.Errorf("redirected to unsupported scheme")
			}
			return nil
		},
	}
}

func safeDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	var selected net.IP
	for _, address := range addresses {
		if isPublicIP(address.IP) {
			selected = address.IP
			break
		}
	}
	if selected == nil {
		return nil, fmt.Errorf("private or local network addresses are blocked")
	}
	dialer := net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(selected.String(), port))
}

func isPublicIP(ip net.IP) bool {
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!ip.IsLinkLocalMulticast() && !ip.IsUnspecified() && !ip.IsMulticast()
}
