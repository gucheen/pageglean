package app

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (a *App) handlePublicFeed(w http.ResponseWriter, r *http.Request) {
	feed, err := a.store.PublicFeed(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	etag := `"` + feed.Revision + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	for _, candidate := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	writeJSON(w, http.StatusOK, feed)
}

type PublicationNotification struct {
	Revision         string `json:"revision"`
	Generation       uint64 `json:"-"`
	EventID          string `json:"-"`
	Attempts         int    `json:"attempts"`
	DueAt            string `json:"dueAt"`
	LastError        string `json:"lastError"`
	NotifiedAt       string `json:"notifiedAt"`
	NotifiedRevision string `json:"notifiedRevision"`
}

func (a *App) refreshPublication(ctx context.Context, force bool) error {
	a.publicationMu.Lock()
	defer a.publicationMu.Unlock()
	feed, err := a.store.PublicFeed(ctx)
	if err != nil {
		return err
	}
	if force || feed.Revision != a.publication.Revision {
		due := time.Now().Add(30 * time.Second)
		if force {
			due = time.Now()
		}
		a.publication.Revision = feed.Revision
		a.publication.Generation++
		a.publication.EventID = rand.Text()
		a.publication.Attempts = 0
		a.publication.DueAt = due.UTC().Format(time.RFC3339Nano)
		a.publication.LastError = ""
	}
	return nil
}

func (a *App) publicationStatus() PublicationNotification {
	a.publicationMu.Lock()
	defer a.publicationMu.Unlock()
	return a.publication
}

// A delivery must not clear a newer change or a manual retry that arrived during the request.
func (a *App) finishPublication(notification PublicationNotification, failure string) {
	a.publicationMu.Lock()
	defer a.publicationMu.Unlock()
	if a.publication.Generation != notification.Generation {
		return
	}
	a.publication.Attempts++
	a.publication.LastError = failure
	a.publication.DueAt = ""
	if failure == "" {
		a.publication.NotifiedAt = time.Now().UTC().Format(time.RFC3339Nano)
		a.publication.NotifiedRevision = notification.Revision
	} else if a.publication.Attempts < 5 {
		a.publication.DueAt = time.Now().Add(30 * time.Second * time.Duration(1<<notification.Attempts)).UTC().Format(time.RFC3339Nano)
	}
}

func (a *App) handlePublicationStatus(w http.ResponseWriter, r *http.Request) {
	configured := a.cfg.WebhookURL != ""
	if configured {
		if err := a.refreshPublication(r.Context(), false); err != nil {
			a.internalError(w, r, err)
			return
		}
	}
	status := a.publicationStatus()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"configured": configured, "feedURL": a.cfg.PublicOrigin + "/public/bookmarks.json", "notification": status})
}

func (a *App) handlePublicationRetry(w http.ResponseWriter, r *http.Request) {
	if a.cfg.WebhookURL == "" {
		writeError(w, http.StatusConflict, "尚未配置更新通知")
		return
	}
	if err := a.refreshPublication(r.Context(), true); err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (a *App) runPublication(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if err := a.processPublication(ctx); err != nil && ctx.Err() == nil {
			a.logger.Error("publication notification failed", "error", err)
		}
		timer.Reset(2 * time.Second)
	}
}

func (a *App) processPublication(ctx context.Context) error {
	if a.cfg.WebhookURL == "" {
		return nil
	}
	if err := a.refreshPublication(ctx, false); err != nil {
		return err
	}
	status := a.publicationStatus()
	if status.DueAt == "" {
		return nil
	}
	due, err := time.Parse(time.RFC3339Nano, status.DueAt)
	if err != nil {
		return err
	}
	if time.Now().Before(due) {
		return nil
	}
	eventID := status.EventID
	body, err := json.Marshal(map[string]any{
		"version": 1, "event": "public_bookmarks.changed", "eventId": eventID,
		"revision": status.Revision, "feedUrl": a.cfg.PublicOrigin + "/public/bookmarks.json",
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("invalid webhook request")
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(a.cfg.WebhookSecret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	if a.cfg.WebhookToken != "" {
		request.Header.Set("Authorization", "Bearer "+a.cfg.WebhookToken)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "PageGlean/1.0")
	request.Header.Set("X-PageGlean-Timestamp", timestamp)
	request.Header.Set("X-PageGlean-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	request.Header.Set("X-PageGlean-Event-ID", eventID)
	response, err := a.webhookClient.Do(request)
	failure := ""
	if err != nil {
		// Transport errors can contain the configured URL, including credentials in its query string.
		failure = "通知请求失败或超时"
	} else {
		io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			failure = fmt.Sprintf("通知接收端返回 HTTP %d", response.StatusCode)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	a.finishPublication(status, failure)
	return nil
}
