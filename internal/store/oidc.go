package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"time"
)

func (s *Store) CreateOIDCLogin(ctx context.Context, session []byte, ttl time.Duration) (string, error) {
	token, raw, err := randomToken()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_logins WHERE expires_at <= ?`, formatTime(s.now())); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO oidc_logins (id_hash, session_json, expires_at) VALUES (?, ?, ?)`, hash[:], session, formatTime(s.now().Add(ttl))); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

// TakeOIDCLogin consumes the request before verification so callbacks cannot be replayed.
func (s *Store) TakeOIDCLogin(ctx context.Context, token string) ([]byte, error) {
	raw, err := decodeToken(token)
	if err != nil {
		return nil, ErrNotFound
	}
	hash := sha256.Sum256(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var session []byte
	var expiresAt string
	if err := tx.QueryRowContext(ctx, `SELECT session_json, expires_at FROM oidc_logins WHERE id_hash = ?`, hash[:]).Scan(&session, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oidc_logins WHERE id_hash = ?`, hash[:]); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	expires, err := parseTime(expiresAt)
	if err != nil || !expires.After(s.now()) {
		return nil, ErrNotFound
	}
	return session, nil
}
