package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestWorkspaceMembershipAndDuplicateSave(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	article, _, err := s.CreateBookmark(ctx, Bookmark{URL: "https://example.com/article", CanonicalURL: "https://example.com/article", Title: "Original title", Public: true, PublicComment: "Recommendation", SkipArchive: true})
	if err != nil || article.Home || !article.Library {
		t.Fatalf("legacy save: %+v, %v", article, err)
	}
	home, duplicate, err := s.CreateBookmark(ctx, Bookmark{URL: article.URL, CanonicalURL: article.CanonicalURL, Home: true, HomeTitle: "Reference", HomeGroup: "Work", HomeOrder: 20, SkipArchive: true})
	if err != nil || !duplicate || home.ID != article.ID || !home.Home || !home.Library || home.HomeTitle != "Reference" || !home.Public || home.PublicComment != "Recommendation" {
		t.Fatalf("duplicate save: %+v, %v", home, err)
	}
	for _, scope := range []string{"home", "library", "all"} {
		items, err := s.ListBookmarks(ctx, BookmarkFilter{Scope: scope})
		if err != nil || len(items) != 1 {
			t.Fatalf("scope %s: %+v, %v", scope, items, err)
		}
	}
	home.Home = false
	retained, err := s.UpdateBookmark(ctx, home)
	if err != nil || !retained.Library || !retained.Public {
		t.Fatalf("remove shortcut: %+v, %v", retained, err)
	}
	items, err := s.ListBookmarks(ctx, BookmarkFilter{Scope: "home"})
	if err != nil || len(items) != 0 {
		t.Fatalf("home: %+v, %v", items, err)
	}
	retained.Library = false
	if _, err := s.UpdateBookmark(ctx, retained); err == nil {
		t.Fatal("allowed orphaned record")
	}
}

func TestHomeOrderAndSearchAcrossScopes(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	for _, item := range []Bookmark{
		{URL: "https://example.com/a", Home: true, HomeTitle: "Alpha", HomeGroup: "Work", HomeOrder: 20},
		{URL: "https://example.com/b", Home: true, HomeTitle: "Beta", HomeGroup: "Work", HomeOrder: 10},
		{URL: "https://example.com/c", Home: true, HomeTitle: "Pinned", HomePinned: true},
		{URL: "https://example.com/d", Title: "Alpha article", Library: true},
	} {
		item.CanonicalURL, item.SkipArchive = item.URL, true
		if _, _, err := s.CreateBookmark(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	items, err := s.ListBookmarks(ctx, BookmarkFilter{Scope: "home"})
	if err != nil || len(items) != 3 || items[0].HomeTitle != "Pinned" || items[1].HomeTitle != "Beta" {
		t.Fatalf("order: %+v, %v", items, err)
	}
	for _, fts := range []bool{true, false} {
		s.ftsEnabled = fts
		for scope, count := range map[string]int{"home": 1, "library": 1, "all": 2} {
			items, err := s.ListBookmarks(ctx, BookmarkFilter{Scope: scope, Query: "Alpha"})
			if err != nil || len(items) != count {
				t.Fatalf("fts=%v, scope=%s: %+v, %v", fts, scope, items, err)
			}
		}
	}
}

func TestBulkMembershipRollsBackOnOrphan(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()
	a, _, err := s.CreateBookmark(ctx, Bookmark{URL: "https://example.com/a", CanonicalURL: "https://example.com/a", Home: true, Library: true, SkipArchive: true})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.CreateBookmark(ctx, Bookmark{URL: "https://example.com/b", CanonicalURL: "https://example.com/b", Home: true, SkipArchive: true})
	if err != nil {
		t.Fatal(err)
	}
	no := false
	if _, err := s.BulkUpdateBookmarks(ctx, []int64{a.ID, b.ID}, BulkBookmarkPatch{Home: &no}); err == nil {
		t.Fatal("expected failure")
	}
	a, err = s.GetBookmark(ctx, a.ID)
	if err != nil || !a.Home {
		t.Fatalf("partial update: %+v, %v", a, err)
	}
}

func TestWorkspaceMigrationPreservesExistingLinks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO bookmarks (url, canonical_url, title, is_public, public_comment, created_at, updated_at, last_seen_at) VALUES ('https://example.com', 'https://example.com', 'Existing', 1, 'Keep', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	for range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		items, err := s.ListBookmarks(context.Background(), BookmarkFilter{Scope: "library"})
		if err != nil || len(items) != 1 || items[0].Home || !items[0].Public || items[0].PublicComment != "Keep" {
			t.Fatalf("migration: %+v, %v", items, err)
		}
		s.Close()
	}
}
