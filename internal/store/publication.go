package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"time"
)

// PublicBookmark is the complete allowlist of fields exposed without authentication.
type PublicBookmark struct {
	URL           string    `json:"url"`
	Title         string    `json:"title"`
	Description   string    `json:"description"`
	PublicComment string    `json:"publicComment"`
	SavedAt       time.Time `json:"savedAt"`
}

type PublicFeed struct {
	Version   int              `json:"version"`
	Revision  string           `json:"revision"`
	Bookmarks []PublicBookmark `json:"bookmarks"`
}

type publicationQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func publicFeed(ctx context.Context, db publicationQuery) (PublicFeed, error) {
	feed := PublicFeed{Version: 1, Bookmarks: []PublicBookmark{}}
	var err error
	feed.Bookmarks, err = publicBookmarks(ctx, db, 6, 0)
	if err != nil {
		return feed, err
	}
	encoded, err := json.Marshal(feed)
	if err != nil {
		return feed, err
	}
	hash := sha256.Sum256(encoded)
	feed.Revision = hex.EncodeToString(hash[:])
	return feed, nil
}

func (s *Store) PublicFeed(ctx context.Context) (PublicFeed, error) {
	return publicFeed(ctx, s.db)
}

func publicBookmarks(ctx context.Context, db publicationQuery, limit, offset int) ([]PublicBookmark, error) {
	items := []PublicBookmark{}
	rows, err := db.QueryContext(ctx, `SELECT url, title, description, public_comment, created_at
		FROM bookmarks WHERE is_public = 1 ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item PublicBookmark
		var savedAt string
		if err := rows.Scan(&item.URL, &item.Title, &item.Description, &item.PublicComment, &savedAt); err != nil {
			return nil, err
		}
		item.SavedAt, err = parseTime(savedAt)
		if err != nil {
			return nil, err
		}
		if item.Title == "" {
			item.Title = item.URL
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) PublicBookmarks(ctx context.Context, limit, offset int) ([]PublicBookmark, error) {
	return publicBookmarks(ctx, s.db, limit, offset)
}
