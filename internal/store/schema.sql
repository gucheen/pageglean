PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    webauthn_handle BLOB NOT NULL UNIQUE,
    name TEXT NOT NULL,
    display_name TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS oidc_logins (
    id_hash BLOB PRIMARY KEY,
    session_json BLOB NOT NULL,
    expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS oidc_app_sessions (
    token_hash BLOB PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    identity TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS bookmarks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    url TEXT NOT NULL,
    canonical_url TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    author TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    is_public INTEGER NOT NULL DEFAULT 0 CHECK (is_public IN (0, 1)),
    public_comment TEXT NOT NULL DEFAULT '',
    unread INTEGER NOT NULL DEFAULT 0 CHECK (unread IN (0, 1)),
    starred INTEGER NOT NULL DEFAULT 0 CHECK (starred IN (0, 1)),
    capture_source TEXT NOT NULL DEFAULT 'web',
    archive_status TEXT NOT NULL DEFAULT 'pending' CHECK (archive_status IN ('idle', 'pending', 'processing', 'complete', 'failed')),
    archive_error TEXT NOT NULL DEFAULT '',
    content_text TEXT NOT NULL DEFAULT '',
    content_path TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    archived_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_bookmarks_created_at ON bookmarks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_bookmarks_unread ON bookmarks(unread, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_bookmarks_starred ON bookmarks(starred, created_at DESC);
CREATE TABLE IF NOT EXISTS archive_jobs (
    bookmark_id INTEGER PRIMARY KEY REFERENCES bookmarks(id) ON DELETE CASCADE,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing')),
    attempts INTEGER NOT NULL DEFAULT 0,
    available_at TEXT NOT NULL,
    started_at TEXT,
    last_error TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_archive_jobs_ready ON archive_jobs(status, available_at);

CREATE TABLE IF NOT EXISTS tags (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS bookmark_tags (
    bookmark_id INTEGER NOT NULL REFERENCES bookmarks(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (bookmark_id, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_bookmark_tags_tag ON bookmark_tags(tag_id, bookmark_id);
