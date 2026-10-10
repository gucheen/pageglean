package store

import (
	"crypto/sha256"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOIDCLoginSingleUseAndExpiry(t *testing.T) {
	s := newTestStore(t)
	token, err := s.CreateOIDCLogin(t.Context(), []byte(`{"nonce":"test"}`), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := s.TakeOIDCLogin(t.Context(), token); err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrNotFound) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d callbacks", accepted.Load())
	}
	token, err = s.CreateOIDCLogin(t.Context(), []byte(`{}`), -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TakeOIDCLogin(t.Context(), token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired login: %v", err)
	}
}

func TestOIDCSessionPersistenceAndLegacyIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pageglean.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	legacy, raw, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	_, err = s.db.Exec(`CREATE TABLE app_sessions (token_hash BLOB PRIMARY KEY, user_id INTEGER, expires_at TEXT); INSERT INTO app_sessions VALUES (?, 1, ?)`, hash[:], formatTime(time.Now().Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.CreateAppSession(t.Context(), 1, time.Hour, "identity")
	if err != nil {
		t.Fatal(err)
	}
	login, err := s.CreateOIDCLogin(t.Context(), []byte(`{"state":"test"}`), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateAppSession(t.Context(), legacy, "identity"); !errors.Is(err, ErrNotFound) {
		t.Fatal("legacy session accepted")
	}
	if id, err := s.ValidateAppSession(t.Context(), session, "identity"); err != nil || id != 1 {
		t.Fatalf("persisted session: %d %v", id, err)
	}
	if _, err := s.ValidateAppSession(t.Context(), session, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong identity accepted")
	}
	if _, err := s.TakeOIDCLogin(t.Context(), login); err != nil {
		t.Fatalf("persisted login: %v", err)
	}
	if err := s.DeleteAppSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidateAppSession(t.Context(), session, "identity"); !errors.Is(err, ErrNotFound) {
		t.Fatal("revoked session accepted")
	}
}
