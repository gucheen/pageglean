package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
	_ "github.com/mattn/go-sqlite3"

	searchindex "pageglean/internal/search"
)

//go:embed schema.sql
var schema string

var ErrNotFound = errors.New("not found")

type Store struct {
	db         *sql.DB
	now        func() time.Time
	ftsEnabled bool
}

type User struct {
	ID          int64
	Handle      []byte
	Name        string
	DisplayName string
	Credentials []webauthn.Credential
}

func (u *User) WebAuthnID() []byte                         { return u.Handle }
func (u *User) WebAuthnName() string                       { return u.Name }
func (u *User) WebAuthnDisplayName() string                { return u.DisplayName }
func (u *User) WebAuthnCredentials() []webauthn.Credential { return u.Credentials }

type Bookmark struct {
	ID            int64      `json:"id"`
	URL           string     `json:"url"`
	CanonicalURL  string     `json:"canonicalUrl"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Author        string     `json:"author"`
	Note          string     `json:"note"`
	Tags          []string   `json:"tags"`
	Unread        bool       `json:"unread"`
	Starred       bool       `json:"starred"`
	CaptureSource string     `json:"captureSource"`
	ArchiveStatus string     `json:"archiveStatus"`
	ArchiveError  string     `json:"archiveError,omitempty"`
	ContentPath   string     `json:"-"`
	ContentHash   string     `json:"contentHash,omitempty"`
	ArchivedAt    *time.Time `json:"archivedAt,omitempty"`
	MatchSnippet  string     `json:"matchSnippet,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	LastSeenAt    time.Time  `json:"lastSeenAt"`
	SkipArchive   bool       `json:"-"`
}

type BookmarkFilter struct {
	Query  string
	State  string
	Limit  int
	Offset int
}

type BulkBookmarkPatch struct {
	AddTags    []string
	RemoveTags []string
	Unread     *bool
	Starred    *bool
}

type ExtensionClient struct {
	ID         int64      `json:"id"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

type ArchiveJob struct {
	BookmarkID int64
	URL        string
	Attempts   int
}

type ArchiveContent struct {
	Title       string
	Description string
	Author      string
	Text        string
	Path        string
	Hash        string
}

type Statistics struct {
	Bookmarks     int64 `json:"bookmarks"`
	Archived      int64 `json:"archived"`
	Pending       int64 `json:"pending"`
	Failed        int64 `json:"failed"`
	DatabaseBytes int64 `json:"databaseBytes"`
	ArchiveBytes  int64 `json:"archiveBytes"`
	FTSEnabled    bool  `json:"ftsEnabled"`
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepathDir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	dsnURL := &url.URL{Scheme: "file", Path: path}
	query := dsnURL.Query()
	query.Set("_busy_timeout", "5000")
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", "NORMAL")
	dsnURL.RawQuery = query.Encode()

	db, err := sql.Open("sqlite3", dsnURL.String())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db, now: time.Now}
	if err := s.init(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func filepathDir(path string) string {
	idx := strings.LastIndexAny(path, `/\`)
	if idx < 0 {
		return "."
	}
	if idx == 0 {
		return path[:1]
	}
	return path[:idx]
}

func (s *Store) init(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := s.ensureBookmarkColumns(ctx); err != nil {
		return err
	}
	if err := s.initFTS(ctx); err != nil {
		return err
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if count == 0 {
		handle, err := randomBytes(64)
		if err != nil {
			return err
		}
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO users (id, webauthn_handle, name, display_name, created_at)
			VALUES (1, ?, 'owner', '拾页用户', ?)
		`, handle, formatTime(s.now()))
		if err != nil {
			return fmt.Errorf("create owner: %w", err)
		}
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM webauthn_ceremonies WHERE expires_at <= ?;
		DELETE FROM app_sessions WHERE expires_at <= ?;
		DELETE FROM admin_tokens WHERE expires_at <= ? OR used_at IS NOT NULL;
	`, formatTime(s.now()), formatTime(s.now()), formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("cleanup expired data: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE archive_jobs SET status = 'pending', started_at = NULL, updated_at = ? WHERE status = 'processing';
		UPDATE bookmarks SET archive_status = 'pending' WHERE archive_status = 'processing';
	`, formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("reset interrupted archive jobs: %w", err)
	}
	return nil
}

func (s *Store) initFTS(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE VIRTUAL TABLE IF NOT EXISTS bookmark_fts USING fts5(
			bookmark_id UNINDEXED, title, tags, note, domain, body,
			tokenize = 'unicode61'
		)
	`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such module: fts5") {
			s.ftsEnabled = false
			return nil
		}
		return fmt.Errorf("create FTS5 index: %w", err)
	}
	s.ftsEnabled = true
	return s.rebuildFTS(ctx)
}

func (s *Store) rebuildFTS(ctx context.Context) error {
	if !s.ftsEnabled {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM bookmark_fts`); err != nil {
		return err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM bookmarks ORDER BY id`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.reindexBookmark(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureBookmarkColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(bookmarks)`)
	if err != nil {
		return fmt.Errorf("inspect bookmark columns: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	columns := []struct{ name, definition string }{
		{"description", `TEXT NOT NULL DEFAULT ''`},
		{"author", `TEXT NOT NULL DEFAULT ''`},
		{"capture_source", `TEXT NOT NULL DEFAULT 'web'`},
		{"archive_status", `TEXT NOT NULL DEFAULT 'pending'`},
		{"archive_error", `TEXT NOT NULL DEFAULT ''`},
		{"content_text", `TEXT NOT NULL DEFAULT ''`},
		{"content_path", `TEXT NOT NULL DEFAULT ''`},
		{"content_hash", `TEXT NOT NULL DEFAULT ''`},
		{"archived_at", `TEXT`},
	}
	for _, column := range columns {
		if existing[column.name] {
			continue
		}
		if _, err := s.db.ExecContext(ctx, `ALTER TABLE bookmarks ADD COLUMN `+column.name+` `+column.definition); err != nil {
			return fmt.Errorf("add bookmark column %s: %w", column.name, err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_bookmarks_archive_status ON bookmarks(archive_status, created_at DESC)`); err != nil {
		return fmt.Errorf("create archive status index: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) LoadOwner(ctx context.Context) (*User, error) {
	return s.loadUser(ctx, `SELECT id, webauthn_handle, name, display_name FROM users WHERE id = 1`)
}

func (s *Store) LoadUserByHandle(ctx context.Context, handle []byte) (*User, error) {
	user, err := s.loadUser(ctx, `SELECT id, webauthn_handle, name, display_name FROM users WHERE webauthn_handle = ?`, handle)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare(user.Handle, handle) != 1 {
		return nil, ErrNotFound
	}
	return user, nil
}

func (s *Store) loadUser(ctx context.Context, query string, args ...any) (*User, error) {
	user := &User{}
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&user.ID, &user.Handle, &user.Name, &user.DisplayName); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load user: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT credential_json FROM webauthn_credentials WHERE user_id = ? ORDER BY created_at
	`, user.ID)
	if err != nil {
		return nil, fmt.Errorf("load credentials: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		var credential webauthn.Credential
		if err := json.Unmarshal(raw, &credential); err != nil {
			return nil, fmt.Errorf("decode credential: %w", err)
		}
		user.Credentials = append(user.Credentials, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credentials: %w", err)
	}
	return user, nil
}

func (s *Store) CredentialCount(ctx context.Context) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM webauthn_credentials`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count credentials: %w", err)
	}
	return count, nil
}

func (s *Store) AddCredential(ctx context.Context, userID int64, rpID, label string, credential *webauthn.Credential) error {
	raw, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode credential: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO webauthn_credentials
		    (credential_id, user_id, rp_id, credential_json, label, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, credential.ID, userID, rpID, raw, label, formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("save credential: %w", err)
	}
	return nil
}

func (s *Store) UpdateCredential(ctx context.Context, credential *webauthn.Credential) error {
	raw, err := json.Marshal(credential)
	if err != nil {
		return fmt.Errorf("encode credential: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE webauthn_credentials
		SET credential_json = ?, last_used_at = ?
		WHERE credential_id = ?
	`, raw, formatTime(s.now()), credential.ID)
	if err != nil {
		return fmt.Errorf("update credential: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateAdminToken(ctx context.Context, kind string, ttl time.Duration) (string, error) {
	if kind != "setup" && kind != "recovery" {
		return "", fmt.Errorf("unsupported admin token kind %q", kind)
	}
	count, err := s.CredentialCount(ctx)
	if err != nil {
		return "", err
	}
	if kind == "setup" && count != 0 {
		return "", fmt.Errorf("setup is already complete; use recovery-link to register another passkey")
	}
	if kind == "recovery" && count == 0 {
		return "", fmt.Errorf("setup is not complete; use setup-link first")
	}
	token, raw, err := randomToken()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO admin_tokens (token_hash, kind, expires_at, created_at)
		VALUES (?, ?, ?, ?)
	`, hash[:], kind, formatTime(now.Add(ttl)), formatTime(now))
	if err != nil {
		return "", fmt.Errorf("save admin token: %w", err)
	}
	return token, nil
}

func (s *Store) ValidateAdminToken(ctx context.Context, token string) ([]byte, string, error) {
	raw, err := decodeToken(token)
	if err != nil {
		return nil, "", ErrNotFound
	}
	hash := sha256.Sum256(raw)
	var kind, expiresAt string
	var usedAt sql.NullString
	if err := s.db.QueryRowContext(ctx, `
		SELECT kind, expires_at, used_at FROM admin_tokens WHERE token_hash = ?
	`, hash[:]).Scan(&kind, &expiresAt, &usedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", ErrNotFound
		}
		return nil, "", err
	}
	expires, err := parseTime(expiresAt)
	if err != nil || !expires.After(s.now()) || usedAt.Valid {
		return nil, "", ErrNotFound
	}
	return hash[:], kind, nil
}

func (s *Store) ConsumeAdminToken(ctx context.Context, hash []byte) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE admin_tokens SET used_at = ?
		WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?
	`, formatTime(s.now()), hash, formatTime(s.now()))
	if err != nil {
		return fmt.Errorf("consume admin token: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateCeremony(ctx context.Context, kind string, session *webauthn.SessionData, adminTokenHash []byte) (string, error) {
	token, raw, err := randomToken()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	encoded, err := json.Marshal(session)
	if err != nil {
		return "", fmt.Errorf("encode WebAuthn session: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO webauthn_ceremonies
		    (id_hash, kind, session_json, admin_token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, hash[:], kind, encoded, nullableBytes(adminTokenHash), formatTime(session.Expires), formatTime(s.now()))
	if err != nil {
		return "", fmt.Errorf("save WebAuthn session: %w", err)
	}
	return token, nil
}

func (s *Store) TakeCeremony(ctx context.Context, token, kind string) (*webauthn.SessionData, []byte, error) {
	raw, err := decodeToken(token)
	if err != nil {
		return nil, nil, ErrNotFound
	}
	hash := sha256.Sum256(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var encoded, adminHash []byte
	var expiresAt string
	if err := tx.QueryRowContext(ctx, `
		SELECT session_json, admin_token_hash, expires_at
		FROM webauthn_ceremonies WHERE id_hash = ? AND kind = ?
	`, hash[:], kind).Scan(&encoded, &adminHash, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM webauthn_ceremonies WHERE id_hash = ?`, hash[:]); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	expires, err := parseTime(expiresAt)
	if err != nil || !expires.After(s.now()) {
		return nil, nil, ErrNotFound
	}
	var session webauthn.SessionData
	if err := json.Unmarshal(encoded, &session); err != nil {
		return nil, nil, fmt.Errorf("decode WebAuthn session: %w", err)
	}
	return &session, adminHash, nil
}

func (s *Store) CreateAppSession(ctx context.Context, userID int64, ttl time.Duration) (string, error) {
	token, raw, err := randomToken()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	now := s.now().UTC()
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO app_sessions (token_hash, user_id, expires_at, created_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?)
	`, hash[:], userID, formatTime(now.Add(ttl)), formatTime(now), formatTime(now))
	if err != nil {
		return "", fmt.Errorf("save app session: %w", err)
	}
	return token, nil
}

func (s *Store) ValidateAppSession(ctx context.Context, token string) (int64, error) {
	raw, err := decodeToken(token)
	if err != nil {
		return 0, ErrNotFound
	}
	hash := sha256.Sum256(raw)
	var userID int64
	var expiresAt string
	if err := s.db.QueryRowContext(ctx, `
		SELECT user_id, expires_at FROM app_sessions WHERE token_hash = ?
	`, hash[:]).Scan(&userID, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, err
	}
	expires, err := parseTime(expiresAt)
	if err != nil || !expires.After(s.now()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM app_sessions WHERE token_hash = ?`, hash[:])
		return 0, ErrNotFound
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE app_sessions SET last_seen_at = ? WHERE token_hash = ?`, formatTime(s.now()), hash[:])
	return userID, nil
}

func (s *Store) DeleteAppSession(ctx context.Context, token string) error {
	raw, err := decodeToken(token)
	if err != nil {
		return nil
	}
	hash := sha256.Sum256(raw)
	_, err = s.db.ExecContext(ctx, `DELETE FROM app_sessions WHERE token_hash = ?`, hash[:])
	return err
}

func (s *Store) CreateExtensionPairing(ctx context.Context, ttl time.Duration) (string, time.Time, error) {
	code, err := randomPairingCode()
	if err != nil {
		return "", time.Time{}, err
	}
	hash := sha256.Sum256([]byte(normalizePairingCode(code)))
	now := s.now().UTC()
	expires := now.Add(ttl)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO extension_pairings (code_hash, expires_at, created_at)
		VALUES (?, ?, ?)
	`, hash[:], formatTime(expires), formatTime(now))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("save extension pairing: %w", err)
	}
	return code, expires, nil
}

func (s *Store) RedeemExtensionPairing(ctx context.Context, code, label string) (string, error) {
	normalized := normalizePairingCode(code)
	if len(normalized) != 8 {
		return "", ErrNotFound
	}
	hash := sha256.Sum256([]byte(normalized))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var expiresAt string
	var usedAt sql.NullString
	if err := tx.QueryRowContext(ctx, `
		SELECT expires_at, used_at FROM extension_pairings WHERE code_hash = ?
	`, hash[:]).Scan(&expiresAt, &usedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	expires, err := parseTime(expiresAt)
	if err != nil || !expires.After(s.now()) || usedAt.Valid {
		return "", ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `UPDATE extension_pairings SET used_at = ? WHERE code_hash = ?`, formatTime(s.now()), hash[:]); err != nil {
		return "", err
	}
	tokenRaw, err := randomBytes(32)
	if err != nil {
		return "", err
	}
	token := "pageglean_cap_" + base64.RawURLEncoding.EncodeToString(tokenRaw)
	tokenHash := sha256.Sum256([]byte(token))
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Chromium"
	}
	if len(label) > 100 {
		label = label[:100]
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO api_tokens (token_hash, label, scope, created_at)
		VALUES (?, ?, 'capture', ?)
	`, tokenHash[:], label, formatTime(s.now()))
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) ValidateCaptureToken(ctx context.Context, token string) error {
	if !strings.HasPrefix(token, "pageglean_cap_") || len(token) < 48 || len(token) > 80 {
		return ErrNotFound
	}
	hash := sha256.Sum256([]byte(token))
	var id int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT id FROM api_tokens
		WHERE token_hash = ? AND scope = 'capture' AND revoked_at IS NULL
	`, hash[:]).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, formatTime(s.now()), id)
	return nil
}

func (s *Store) ListExtensionClients(ctx context.Context) ([]ExtensionClient, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, label, created_at, last_used_at FROM api_tokens
		WHERE scope = 'capture' AND revoked_at IS NULL ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	clients := []ExtensionClient{}
	for rows.Next() {
		var client ExtensionClient
		var createdAt string
		var lastUsed sql.NullString
		if err := rows.Scan(&client.ID, &client.Label, &createdAt, &lastUsed); err != nil {
			return nil, err
		}
		client.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			parsed, err := parseTime(lastUsed.String)
			if err != nil {
				return nil, err
			}
			client.LastUsedAt = &parsed
		}
		clients = append(clients, client)
	}
	return clients, rows.Err()
}

func (s *Store) RevokeExtensionClient(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND scope = 'capture' AND revoked_at IS NULL
	`, formatTime(s.now()), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ClaimArchiveJob(ctx context.Context) (*ArchiveJob, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	job := &ArchiveJob{}
	err = tx.QueryRowContext(ctx, `
		SELECT j.bookmark_id, b.url, j.attempts
		FROM archive_jobs j JOIN bookmarks b ON b.id = j.bookmark_id
		WHERE j.status = 'pending' AND j.available_at <= ?
		ORDER BY j.available_at, j.bookmark_id LIMIT 1
	`, formatTime(s.now())).Scan(&job.BookmarkID, &job.URL, &job.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	now := formatTime(s.now())
	if _, err := tx.ExecContext(ctx, `
		UPDATE archive_jobs SET status = 'processing', started_at = ?, updated_at = ? WHERE bookmark_id = ?
	`, now, now, job.BookmarkID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE bookmarks SET archive_status = 'processing', archive_error = '' WHERE id = ?
	`, job.BookmarkID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return job, nil
}

func (s *Store) CompleteArchive(ctx context.Context, bookmarkID int64, content ArchiveContent) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := formatTime(s.now())
	_, err = tx.ExecContext(ctx, `
		UPDATE bookmarks
		SET title = CASE WHEN title = '' THEN ? ELSE title END,
		    description = ?, author = ?, content_text = ?, content_path = ?, content_hash = ?,
		    archive_status = 'complete', archive_error = '', archived_at = ?, updated_at = ?
		WHERE id = ?
	`, content.Title, content.Description, content.Author, content.Text, content.Path, content.Hash, now, now, bookmarkID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM archive_jobs WHERE bookmark_id = ?`, bookmarkID); err != nil {
		return err
	}
	if err := s.reindexBookmarkTx(ctx, tx, bookmarkID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FailArchive(ctx context.Context, bookmarkID int64, message string) error {
	if len(message) > 500 {
		message = message[:500]
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var attempts int
	if err := tx.QueryRowContext(ctx, `SELECT attempts FROM archive_jobs WHERE bookmark_id = ?`, bookmarkID).Scan(&attempts); err != nil {
		return err
	}
	attempts++
	now := s.now().UTC()
	if attempts >= 3 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM archive_jobs WHERE bookmark_id = ?`, bookmarkID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE bookmarks SET archive_status = 'failed', archive_error = ?, updated_at = ? WHERE id = ?
		`, message, formatTime(now), bookmarkID); err != nil {
			return err
		}
	} else {
		backoff := time.Duration(1<<attempts) * time.Minute
		if _, err := tx.ExecContext(ctx, `
			UPDATE archive_jobs
			SET status = 'pending', attempts = ?, available_at = ?, started_at = NULL,
			    last_error = ?, updated_at = ? WHERE bookmark_id = ?
		`, attempts, formatTime(now.Add(backoff)), message, formatTime(now), bookmarkID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE bookmarks SET archive_status = 'pending', archive_error = ?, updated_at = ? WHERE id = ?
		`, message, formatTime(now), bookmarkID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RetryArchive(ctx context.Context, bookmarkID int64) error {
	bookmark, err := s.GetBookmark(ctx, bookmarkID)
	if err != nil {
		return err
	}
	if bookmark.ArchiveStatus == "complete" {
		return nil
	}
	now := formatTime(s.now())
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO archive_jobs (bookmark_id, status, attempts, available_at, created_at, updated_at)
		VALUES (?, 'pending', 0, ?, ?, ?)
		ON CONFLICT(bookmark_id) DO UPDATE SET status = 'pending', attempts = 0,
		    available_at = excluded.available_at, started_at = NULL, last_error = '', updated_at = excluded.updated_at;
		UPDATE bookmarks SET archive_status = 'pending', archive_error = '' WHERE id = ?;
	`, bookmarkID, now, now, now, bookmarkID)
	return err
}

func randomPairingCode() (string, error) {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	raw, err := randomBytes(8)
	if err != nil {
		return "", err
	}
	code := make([]byte, 8)
	for index, value := range raw {
		code[index] = alphabet[int(value)%len(alphabet)]
	}
	return string(code[:4]) + "-" + string(code[4:]), nil
}

func normalizePairingCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}

func (s *Store) setBookmarkTagsTx(ctx context.Context, tx *sql.Tx, bookmarkID int64, values []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM bookmark_tags WHERE bookmark_id = ?`, bookmarkID); err != nil {
		return err
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		name := strings.TrimSpace(value)
		if name == "" {
			continue
		}
		if len([]rune(name)) > 50 {
			return fmt.Errorf("tag is longer than 50 characters")
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if len(seen) > 50 {
			return fmt.Errorf("a bookmark may have at most 50 tags")
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO tags (name, created_at) VALUES (?, ?)`, name, formatTime(s.now())); err != nil {
			return err
		}
		var tagID int64
		if err := tx.QueryRowContext(ctx, `SELECT id FROM tags WHERE name = ? COLLATE NOCASE`, name).Scan(&tagID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO bookmark_tags (bookmark_id, tag_id) VALUES (?, ?)`, bookmarkID, tagID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) loadBookmarkTags(ctx context.Context, bookmarkID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.name FROM tags t JOIN bookmark_tags bt ON bt.tag_id = t.id
		WHERE bt.bookmark_id = ? ORDER BY t.name COLLATE NOCASE
	`, bookmarkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tags = append(tags, name)
	}
	return tags, rows.Err()
}

func (s *Store) reindexBookmark(ctx context.Context, bookmarkID int64) error {
	if !s.ftsEnabled {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.reindexBookmarkTx(ctx, tx, bookmarkID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) reindexBookmarkTx(ctx context.Context, tx *sql.Tx, bookmarkID int64) error {
	if !s.ftsEnabled {
		return nil
	}
	var title, note, rawURL, body string
	if err := tx.QueryRowContext(ctx, `
		SELECT title, note, url, content_text FROM bookmarks WHERE id = ?
	`, bookmarkID).Scan(&title, &note, &rawURL, &body); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT t.name FROM tags t JOIN bookmark_tags bt ON bt.tag_id = t.id
		WHERE bt.bookmark_id = ? ORDER BY t.name COLLATE NOCASE
	`, bookmarkID)
	if err != nil {
		return err
	}
	var tags []string
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			rows.Close()
			return err
		}
		tags = append(tags, tag)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	domain := rawURL
	if parsed, err := url.Parse(rawURL); err == nil && parsed.Hostname() != "" {
		domain = parsed.Hostname()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM bookmark_fts WHERE bookmark_id = ?`, bookmarkID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO bookmark_fts (bookmark_id, title, tags, note, domain, body)
		VALUES (?, ?, ?, ?, ?, ?)
	`, bookmarkID, searchindex.Document(title), searchindex.Document(strings.Join(tags, " ")),
		searchindex.Document(note), searchindex.Document(domain), searchindex.Document(body))
	return err
}

func (s *Store) searchSnippet(ctx context.Context, bookmarkID int64, needle string) (string, error) {
	if strings.TrimSpace(needle) == "" {
		return "", nil
	}
	var snippet string
	err := s.db.QueryRowContext(ctx, `
		SELECT CASE
			WHEN instr(lower(content_text), lower(?)) > 90
			THEN '…' || substr(content_text, instr(lower(content_text), lower(?)) - 80, 220) || '…'
			WHEN instr(lower(content_text), lower(?)) > 0
			THEN substr(content_text, 1, 220) || CASE WHEN length(content_text) > 220 THEN '…' ELSE '' END
			ELSE '' END
		FROM bookmarks WHERE id = ?
	`, needle, needle, needle, bookmarkID).Scan(&snippet)
	return strings.TrimSpace(snippet), err
}

func (s *Store) FTSEnabled() bool { return s.ftsEnabled }

func (s *Store) Stats(ctx context.Context) (Statistics, error) {
	stats := Statistics{FTSEnabled: s.ftsEnabled}
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN archive_status = 'complete' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN archive_status IN ('pending', 'processing') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN archive_status = 'failed' THEN 1 ELSE 0 END), 0)
		FROM bookmarks
	`).Scan(&stats.Bookmarks, &stats.Archived, &stats.Pending, &stats.Failed)
	if err != nil {
		return Statistics{}, err
	}
	return stats, nil
}

func (s *Store) BackupDatabase(ctx context.Context, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return fmt.Errorf("backup database destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return fmt.Errorf("create SQLite snapshot: %w", err)
	}
	return nil
}

func (s *Store) CreateBookmark(ctx context.Context, bookmark Bookmark) (Bookmark, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Bookmark{}, false, err
	}
	defer tx.Rollback()

	var existingID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM bookmarks WHERE canonical_url = ?`, bookmark.CanonicalURL).Scan(&existingID)
	if err == nil {
		_, err = tx.ExecContext(ctx, `
			UPDATE bookmarks
			SET last_seen_at = ?,
			    title = CASE WHEN title = '' THEN ? ELSE title END,
			    note = CASE WHEN note = '' THEN ? ELSE note END,
			    updated_at = CASE WHEN (title = '' AND ? != '') OR (note = '' AND ? != '') THEN ? ELSE updated_at END
			WHERE id = ?
		`, formatTime(s.now()), bookmark.Title, bookmark.Note, bookmark.Title, bookmark.Note, formatTime(s.now()), existingID)
		if err != nil {
			return Bookmark{}, false, err
		}
		if len(bookmark.Tags) > 0 {
			if err := s.setBookmarkTagsTx(ctx, tx, existingID, bookmark.Tags); err != nil {
				return Bookmark{}, false, err
			}
		}
		if !bookmark.SkipArchive {
			_, err = tx.ExecContext(ctx, `
				INSERT OR IGNORE INTO archive_jobs
				    (bookmark_id, status, attempts, available_at, created_at, updated_at)
				SELECT id, 'pending', 0, ?, ?, ? FROM bookmarks
				WHERE id = ? AND archive_status != 'complete'
			`, formatTime(s.now()), formatTime(s.now()), formatTime(s.now()), existingID)
			if err != nil {
				return Bookmark{}, false, err
			}
		}
		if err := s.reindexBookmarkTx(ctx, tx, existingID); err != nil {
			return Bookmark{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return Bookmark{}, false, err
		}
		existing, err := s.GetBookmark(ctx, existingID)
		return existing, true, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Bookmark{}, false, err
	}

	now := s.now().UTC()
	createdAt := bookmark.CreatedAt.UTC()
	if createdAt.IsZero() || createdAt.After(now) {
		createdAt = now
	}
	if bookmark.CaptureSource == "" {
		bookmark.CaptureSource = "web"
	}
	archiveStatus := "pending"
	if bookmark.SkipArchive {
		archiveStatus = "idle"
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO bookmarks
		    (url, canonical_url, title, description, author, note, unread, starred, capture_source,
		     archive_status, created_at, updated_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, bookmark.URL, bookmark.CanonicalURL, bookmark.Title, bookmark.Description, bookmark.Author,
		bookmark.Note, bookmark.Unread, bookmark.Starred, bookmark.CaptureSource, archiveStatus,
		formatTime(createdAt), formatTime(now), formatTime(now))
	if err != nil {
		return Bookmark{}, false, fmt.Errorf("insert bookmark: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return Bookmark{}, false, err
	}
	if !bookmark.SkipArchive {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO archive_jobs (bookmark_id, status, attempts, available_at, created_at, updated_at)
			VALUES (?, 'pending', 0, ?, ?, ?)
		`, id, formatTime(now), formatTime(now), formatTime(now))
		if err != nil {
			return Bookmark{}, false, fmt.Errorf("enqueue archive: %w", err)
		}
	}
	if err := s.setBookmarkTagsTx(ctx, tx, id, bookmark.Tags); err != nil {
		return Bookmark{}, false, err
	}
	if err := s.reindexBookmarkTx(ctx, tx, id); err != nil {
		return Bookmark{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Bookmark{}, false, err
	}
	created, err := s.GetBookmark(ctx, id)
	return created, false, err
}

func (s *Store) GetBookmark(ctx context.Context, id int64) (Bookmark, error) {
	bookmark, err := scanBookmark(s.db.QueryRowContext(ctx, `
		SELECT id, url, canonical_url, title, description, author, note, unread, starred,
		       capture_source, archive_status, archive_error, content_path, content_hash, archived_at,
		       created_at, updated_at, last_seen_at
		FROM bookmarks WHERE id = ?
	`, id))
	if err != nil {
		return Bookmark{}, err
	}
	bookmark.Tags, err = s.loadBookmarkTags(ctx, id)
	return bookmark, err
}

func (s *Store) ListBookmarks(ctx context.Context, filter BookmarkFilter) ([]Bookmark, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	var where []string
	var args []any
	searchQuery := strings.TrimSpace(filter.Query)
	useFTS := searchQuery != "" && s.ftsEnabled && searchindex.Query(searchQuery) != ""
	if searchQuery != "" && !useFTS {
		where = append(where, `(b.title LIKE ? ESCAPE '\' OR b.url LIKE ? ESCAPE '\' OR b.note LIKE ? ESCAPE '\' OR b.content_text LIKE ? ESCAPE '\')`)
		pattern := "%" + escapeLike(searchQuery) + "%"
		args = append(args, pattern, pattern, pattern, pattern)
	}
	switch filter.State {
	case "unread":
		where = append(where, `b.unread = 1`)
	case "starred":
		where = append(where, `b.starred = 1`)
	}
	query := `
		SELECT b.id, b.url, b.canonical_url, b.title, b.description, b.author, b.note, b.unread, b.starred,
		       capture_source, archive_status, archive_error, content_path, content_hash, archived_at,
		       created_at, updated_at, last_seen_at
		FROM bookmarks b`
	if useFTS {
		query += ` JOIN bookmark_fts ON bookmark_fts.bookmark_id = b.id`
		where = append(where, `bookmark_fts MATCH ?`)
		args = append(args, searchindex.Query(searchQuery))
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	if useFTS {
		query += ` ORDER BY bm25(bookmark_fts, 0.0, 8.0, 6.0, 5.0, 3.0, 1.0), b.created_at DESC LIMIT ? OFFSET ?`
	} else {
		query += ` ORDER BY b.created_at DESC, b.id DESC LIMIT ? OFFSET ?`
	}
	args = append(args, limit, filter.Offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list bookmarks: %w", err)
	}
	defer rows.Close()
	bookmarks := make([]Bookmark, 0)
	for rows.Next() {
		bookmark, err := scanBookmark(rows)
		if err != nil {
			return nil, err
		}
		bookmarks = append(bookmarks, bookmark)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range bookmarks {
		bookmarks[index].Tags, err = s.loadBookmarkTags(ctx, bookmarks[index].ID)
		if err != nil {
			return nil, err
		}
		if searchQuery != "" {
			bookmarks[index].MatchSnippet, err = s.searchSnippet(ctx, bookmarks[index].ID, searchindex.SnippetNeedle(searchQuery))
			if err != nil {
				return nil, err
			}
		}
	}
	return bookmarks, rows.Err()
}

func (s *Store) UpdateBookmark(ctx context.Context, bookmark Bookmark) (Bookmark, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Bookmark{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE bookmarks
		SET title = ?, note = ?, unread = ?, starred = ?, updated_at = ?
		WHERE id = ?
	`, bookmark.Title, bookmark.Note, bookmark.Unread, bookmark.Starred, formatTime(s.now()), bookmark.ID)
	if err != nil {
		return Bookmark{}, fmt.Errorf("update bookmark: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return Bookmark{}, ErrNotFound
	}
	if err := s.setBookmarkTagsTx(ctx, tx, bookmark.ID, bookmark.Tags); err != nil {
		return Bookmark{}, err
	}
	if err := s.reindexBookmarkTx(ctx, tx, bookmark.ID); err != nil {
		return Bookmark{}, err
	}
	if err := tx.Commit(); err != nil {
		return Bookmark{}, err
	}
	return s.GetBookmark(ctx, bookmark.ID)
}

func (s *Store) BulkUpdateBookmarks(ctx context.Context, ids []int64, patch BulkBookmarkPatch) (int, error) {
	ids = uniquePositiveIDs(ids)
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	unread := optionalBool(patch.Unread)
	starred := optionalBool(patch.Starred)
	updated := 0
	for _, id := range ids {
		result, err := tx.ExecContext(ctx, `
			UPDATE bookmarks SET
				unread = CASE WHEN ? IS NULL THEN unread ELSE ? END,
				starred = CASE WHEN ? IS NULL THEN starred ELSE ? END,
				updated_at = ?
			WHERE id = ?
		`, unread, unread, starred, starred, formatTime(s.now()), id)
		if err != nil {
			return 0, err
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			continue
		}
		updated++
		if len(patch.AddTags) > 0 || len(patch.RemoveTags) > 0 {
			tags, err := loadBookmarkTagsTx(ctx, tx, id)
			if err != nil {
				return 0, err
			}
			remove := map[string]struct{}{}
			for _, tag := range patch.RemoveTags {
				remove[strings.ToLower(strings.TrimSpace(tag))] = struct{}{}
			}
			merged := make([]string, 0, len(tags)+len(patch.AddTags))
			for _, tag := range tags {
				if _, found := remove[strings.ToLower(tag)]; !found {
					merged = append(merged, tag)
				}
			}
			merged = append(merged, patch.AddTags...)
			if err := s.setBookmarkTagsTx(ctx, tx, id, merged); err != nil {
				return 0, err
			}
			if err := s.reindexBookmarkTx(ctx, tx, id); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return updated, nil
}

func (s *Store) BulkDeleteBookmarks(ctx context.Context, ids []int64) (int, error) {
	ids = uniquePositiveIDs(ids)
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	deleted := 0
	for _, id := range ids {
		if s.ftsEnabled {
			if _, err := tx.ExecContext(ctx, `DELETE FROM bookmark_fts WHERE bookmark_id = ?`, id); err != nil {
				return 0, err
			}
		}
		result, err := tx.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ?`, id)
		if err != nil {
			return 0, err
		}
		affected, _ := result.RowsAffected()
		deleted += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

func optionalBool(value *bool) any {
	if value == nil {
		return nil
	}
	return *value
}

func uniquePositiveIDs(values []int64) []int64 {
	result := make([]int64, 0, len(values))
	seen := map[int64]struct{}{}
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func loadBookmarkTagsTx(ctx context.Context, tx *sql.Tx, bookmarkID int64) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT t.name FROM tags t JOIN bookmark_tags bt ON bt.tag_id = t.id
		WHERE bt.bookmark_id = ? ORDER BY t.name COLLATE NOCASE
	`, bookmarkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tags := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tags = append(tags, name)
	}
	return tags, rows.Err()
}

func (s *Store) DeleteBookmark(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.ftsEnabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM bookmark_fts WHERE bookmark_id = ?`, id); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM bookmarks WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete bookmark: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanBookmark(row scanner) (Bookmark, error) {
	var bookmark Bookmark
	var unread, starred bool
	var createdAt, updatedAt, lastSeenAt string
	var archivedAt sql.NullString
	err := row.Scan(
		&bookmark.ID, &bookmark.URL, &bookmark.CanonicalURL, &bookmark.Title, &bookmark.Description,
		&bookmark.Author, &bookmark.Note, &unread, &starred, &bookmark.CaptureSource,
		&bookmark.ArchiveStatus, &bookmark.ArchiveError, &bookmark.ContentPath, &bookmark.ContentHash,
		&archivedAt, &createdAt, &updatedAt, &lastSeenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Bookmark{}, ErrNotFound
	}
	if err != nil {
		return Bookmark{}, err
	}
	bookmark.Unread = unread
	bookmark.Starred = starred
	if archivedAt.Valid {
		parsed, parseErr := parseTime(archivedAt.String)
		if parseErr != nil {
			return Bookmark{}, parseErr
		}
		bookmark.ArchivedAt = &parsed
	}
	if bookmark.CreatedAt, err = parseTime(createdAt); err != nil {
		return Bookmark{}, err
	}
	if bookmark.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return Bookmark{}, err
	}
	if bookmark.LastSeenAt, err = parseTime(lastSeenAt); err != nil {
		return Bookmark{}, err
	}
	return bookmark, nil
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func formatTime(value time.Time) string         { return value.UTC().Format(time.RFC3339Nano) }
func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }

func randomBytes(size int) ([]byte, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return nil, fmt.Errorf("read crypto random bytes: %w", err)
	}
	return value, nil
}

func randomToken() (string, []byte, error) {
	raw, err := randomBytes(32)
	if err != nil {
		return "", nil, err
	}
	return base64.RawURLEncoding.EncodeToString(raw), raw, nil
}

func decodeToken(token string) ([]byte, error) {
	if len(token) < 40 || len(token) > 64 {
		return nil, errors.New("invalid token length")
	}
	return base64.RawURLEncoding.DecodeString(token)
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}
