package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "pageglean.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOwnerIsCreatedOnce(t *testing.T) {
	s := newTestStore(t)
	user, err := s.LoadOwner(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(user.Handle) != 64 {
		t.Fatalf("handle length = %d", len(user.Handle))
	}
	if len(user.Credentials) != 0 {
		t.Fatalf("credentials = %d", len(user.Credentials))
	}
}

func TestOpenConfiguresSQLite(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	var foreignKeys, busyTimeout, synchronous int
	var journalMode string
	for query, target := range map[string]any{
		"PRAGMA foreign_keys": &foreignKeys,
		"PRAGMA busy_timeout": &busyTimeout,
		"PRAGMA journal_mode": &journalMode,
		"PRAGMA synchronous":  &synchronous,
	} {
		if err := s.db.QueryRowContext(ctx, query).Scan(target); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	if foreignKeys != 1 || busyTimeout != 5000 || journalMode != "wal" || synchronous != 1 {
		t.Fatalf("unexpected SQLite configuration: foreign_keys=%d busy_timeout=%d journal_mode=%q synchronous=%d", foreignKeys, busyTimeout, journalMode, synchronous)
	}
	if !s.FTSEnabled() {
		t.Fatal("FTS5 is not enabled")
	}
}

func TestCreateBookmarkDeduplicatesCanonicalURL(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first, duplicate, err := s.CreateBookmark(ctx, Bookmark{
		URL: "https://example.com/article#intro", CanonicalURL: "https://example.com/article", Title: "First",
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("first insert reported duplicate")
	}
	second, duplicate, err := s.CreateBookmark(ctx, Bookmark{
		URL: "https://example.com/article", CanonicalURL: "https://example.com/article", Title: "Second",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate {
		t.Fatal("second insert did not report duplicate")
	}
	if first.ID != second.ID || second.Title != "First" {
		t.Fatalf("unexpected duplicate result: %#v", second)
	}
}

func TestListBookmarksFiltersAndSearches(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _, _ = s.CreateBookmark(ctx, Bookmark{
		URL: "https://example.com/zh", CanonicalURL: "https://example.com/zh", Title: "中文搜索", Unread: true,
	})
	_, _, _ = s.CreateBookmark(ctx, Bookmark{
		URL: "https://example.com/go", CanonicalURL: "https://example.com/go", Title: "Go notes", Starred: true,
	})

	items, err := s.ListBookmarks(ctx, BookmarkFilter{Query: "中文", State: "unread"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Title != "中文搜索" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestFTSPrefixSearchMatchesLatinTokenPrefixes(t *testing.T) {
	s := newTestStore(t)
	if !s.FTSEnabled() {
		t.Skip("FTS5 is not enabled")
	}

	bookmark, _, err := s.CreateBookmark(context.Background(), Bookmark{
		URL: "https://example.com/tome4", CanonicalURL: "https://example.com/tome4",
		Title: "Tome4 guide",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteArchive(context.Background(), bookmark.ID, ArchiveContent{
		Text: "A guide to Tome4", Path: "tome4", Hash: "tome4",
	}); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListBookmarks(context.Background(), BookmarkFilter{Query: "tome", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != bookmark.ID {
		t.Fatalf("unexpected prefix search results: %#v", items)
	}
}

func TestAdminTokenRules(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	token, err := s.CreateAdminToken(ctx, "setup", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	hash, kind, err := s.ValidateAdminToken(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if kind != "setup" {
		t.Fatalf("kind = %q", kind)
	}
	if err := s.ConsumeAdminToken(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ValidateAdminToken(ctx, token); err == nil {
		t.Fatal("consumed token remained valid")
	}
}

func TestExtensionPairingIsOneTimeAndCaptureOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	code, expires, err := s.CreateExtensionPairing(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !expires.After(time.Now()) {
		t.Fatal("pairing code is already expired")
	}
	token, err := s.RedeemExtensionPairing(ctx, code, "My Chromium")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateCaptureToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RedeemExtensionPairing(ctx, code, "Second client"); err == nil {
		t.Fatal("pairing code was accepted twice")
	}
	clients, err := s.ListExtensionClients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 || clients[0].Label != "My Chromium" {
		t.Fatalf("unexpected clients: %#v", clients)
	}
	if err := s.RevokeExtensionClient(ctx, clients[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateCaptureToken(ctx, token); err == nil {
		t.Fatal("revoked token remained valid")
	}
}

func TestOpenMigratesStageOneBookmarkTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pageglean.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		CREATE TABLE bookmarks (
			id INTEGER PRIMARY KEY AUTOINCREMENT, url TEXT NOT NULL, canonical_url TEXT NOT NULL UNIQUE,
			title TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', unread INTEGER NOT NULL DEFAULT 0,
			starred INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, last_seen_at TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	created, _, err := s.CreateBookmark(context.Background(), Bookmark{
		URL: "https://example.com", CanonicalURL: "https://example.com/", Title: "Migrated",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ArchiveStatus != "pending" {
		t.Fatalf("archive status = %q", created.ArchiveStatus)
	}
}

func TestChineseFTSIndexesTitleTagsAndArchivedBody(t *testing.T) {
	s := newTestStore(t)
	if !s.FTSEnabled() {
		t.Skip("FTS5 is not enabled in this build")
	}
	ctx := context.Background()
	first, _, err := s.CreateBookmark(ctx, Bookmark{
		URL: "https://example.com/title", CanonicalURL: "https://example.com/title",
		Title: "中文搜索指南", Tags: []string{"知识管理"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteArchive(ctx, first.ID, ArchiveContent{Text: "标题命中的正文较短", Path: "a", Hash: "a"}); err != nil {
		t.Fatal(err)
	}
	second, _, err := s.CreateBookmark(ctx, Bookmark{
		URL: "https://example.com/body", CanonicalURL: "https://example.com/body", Title: "另一篇文章",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteArchive(ctx, second.ID, ArchiveContent{
		Text: "这篇正文也讨论中文搜索、SQLite 和全文检索的实现。", Path: "b", Hash: "b",
	}); err != nil {
		t.Fatal(err)
	}

	items, err := s.ListBookmarks(ctx, BookmarkFilter{Query: "中文搜索", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != first.ID {
		t.Fatalf("unexpected ranked results: %#v", items)
	}
	bodyItems, err := s.ListBookmarks(ctx, BookmarkFilter{Query: "全文检索", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodyItems) != 1 || bodyItems[0].ID != second.ID || bodyItems[0].MatchSnippet == "" {
		t.Fatalf("unexpected body results: %#v", bodyItems)
	}
}

func TestImportedBookmarkCanSkipArchiveAndPreserveDate(t *testing.T) {
	s := newTestStore(t)
	createdAt := time.Date(2022, 3, 4, 5, 6, 7, 0, time.UTC)
	bookmark, _, err := s.CreateBookmark(context.Background(), Bookmark{
		URL: "https://example.com/imported", CanonicalURL: "https://example.com/imported",
		Title: "Imported", CaptureSource: "import", SkipArchive: true, CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bookmark.ArchiveStatus != "idle" || !bookmark.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected imported bookmark: %#v", bookmark)
	}
	if job, err := s.ClaimArchiveJob(context.Background()); err == nil || job != nil {
		t.Fatalf("import unexpectedly created an archive job: %#v", job)
	}
	if err := s.RetryArchive(context.Background(), bookmark.ID); err != nil {
		t.Fatal(err)
	}
	job, err := s.ClaimArchiveJob(context.Background())
	if err != nil || job.BookmarkID != bookmark.ID {
		t.Fatalf("manual archive did not enqueue imported bookmark: job=%#v err=%v", job, err)
	}
}

func TestBulkUpdateAndDeleteBookmarks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first, _, _ := s.CreateBookmark(ctx, Bookmark{
		URL: "https://example.com/one", CanonicalURL: "https://example.com/one", Tags: []string{"旧标签"},
	})
	second, _, _ := s.CreateBookmark(ctx, Bookmark{
		URL: "https://example.com/two", CanonicalURL: "https://example.com/two", Tags: []string{"保留"},
	})
	unread := true
	updated, err := s.BulkUpdateBookmarks(ctx, []int64{first.ID, second.ID, first.ID}, BulkBookmarkPatch{
		AddTags: []string{"批量"}, RemoveTags: []string{"旧标签"}, Unread: &unread,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated != 2 {
		t.Fatalf("updated = %d, want 2", updated)
	}
	got, err := s.GetBookmark(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Unread || len(got.Tags) != 1 || got.Tags[0] != "批量" {
		t.Fatalf("unexpected bulk update: %#v", got)
	}
	deleted, err := s.BulkDeleteBookmarks(ctx, []int64{first.ID, second.ID})
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}
}
