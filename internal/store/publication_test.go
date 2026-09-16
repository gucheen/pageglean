package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicFeedOrderingPrivacyAndWithdrawal(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	base := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	var saved []Bookmark
	for i := range 8 {
		url := fmt.Sprintf("https://example.com/%d", i)
		b, _, err := s.CreateBookmark(ctx, Bookmark{URL: url, CanonicalURL: url, Title: fmt.Sprint(i), Public: true,
			Description: "page metadata", Note: "PRIVATE_NOTE", PublicComment: "my recommendation", Tags: []string{"PRIVATE_TAG"},
			CreatedAt: base.Add(time.Duration(i) * time.Hour), SkipArchive: true})
		if err != nil {
			t.Fatal(err)
		}
		saved = append(saved, b)
	}
	_, _, err := s.CreateBookmark(ctx, Bookmark{URL: "https://private.example", CanonicalURL: "https://private.example", Title: "PRIVATE_TITLE", Starred: true, SkipArchive: true})
	if err != nil {
		t.Fatal(err)
	}
	feed, err := s.PublicFeed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Bookmarks) != 6 || feed.Bookmarks[0].Title != "7" || feed.Bookmarks[5].Title != "2" {
		t.Fatalf("feed: %#v", feed)
	}
	encoded, _ := json.Marshal(feed)
	if strings.Contains(string(encoded), "PRIVATE") {
		t.Fatalf("private data in feed: %s", encoded)
	}
	var payload map[string]any
	json.Unmarshal(encoded, &payload)
	for _, item := range payload["bookmarks"].([]any) {
		fields := item.(map[string]any)
		if len(fields) != 5 {
			t.Fatalf("unexpected public fields: %#v", fields)
		}
	}
	b := saved[7]
	b.Note = "changed private note"
	b.Starred = true
	if _, err := s.UpdateBookmark(ctx, b); err != nil {
		t.Fatal(err)
	}
	same, _ := s.PublicFeed(ctx)
	if same.Revision != feed.Revision {
		t.Fatal("private edit changed public revision")
	}
	b.Public = false
	if _, err := s.UpdateBookmark(ctx, b); err != nil {
		t.Fatal(err)
	}
	withdrawn, _ := s.PublicFeed(ctx)
	if withdrawn.Bookmarks[0].Title != "6" || withdrawn.Bookmarks[5].Title != "1" || withdrawn.Revision == feed.Revision {
		t.Fatalf("withdrawal: %#v", withdrawn)
	}
	if _, err := s.BulkDeleteBookmarks(ctx, []int64{saved[6].ID}); err != nil {
		t.Fatal(err)
	}
	deleted, _ := s.PublicFeed(ctx)
	if deleted.Bookmarks[0].Title != "5" || deleted.Bookmarks[5].Title != "0" {
		t.Fatalf("delete: %#v", deleted)
	}
	_, duplicate, err := s.CreateBookmark(ctx, Bookmark{URL: b.URL, CanonicalURL: b.CanonicalURL, Public: true, PublicComment: "should not publish", SkipArchive: true})
	if err != nil || !duplicate {
		t.Fatalf("duplicate: %v %v", duplicate, err)
	}
	stillPrivate, _ := s.GetBookmark(ctx, b.ID)
	if stillPrivate.Public {
		t.Fatal("duplicate save republished a private bookmark")
	}
}

func TestPublicFieldsSurviveRestartAndOldBookmarksStayPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	oldSchema := strings.ReplaceAll(schema, "    is_public INTEGER NOT NULL DEFAULT 0 CHECK (is_public IN (0, 1)),\n", "")
	oldSchema = strings.ReplaceAll(oldSchema, "    public_comment TEXT NOT NULL DEFAULT '',\n", "")
	if _, err := db.Exec(oldSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO bookmarks(url,canonical_url,title,note,created_at,updated_at,last_seen_at) VALUES('https://old.example','https://old.example','old','private','2025-01-01T00:00:00Z','2025-01-01T00:00:00Z','2025-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.GetBookmark(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if b.Public || b.PublicComment != "" || b.Note != "private" {
		t.Fatalf("migration: %#v", b)
	}
	b.Public = true
	b.PublicComment = "公开短评"
	if _, err := s.UpdateBookmark(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	feed, err := s.PublicFeed(t.Context())
	if err != nil || len(feed.Bookmarks) != 1 || feed.Bookmarks[0].PublicComment != "公开短评" {
		t.Fatalf("restart: %#v %v", feed, err)
	}
}
