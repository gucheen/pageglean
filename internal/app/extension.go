package app

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"links/internal/bookmarks"
	"links/internal/store"
)

type extensionPairRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"deviceName"`
}

type captureRequest struct {
	URL          string `json:"url"`
	CanonicalURL string `json:"canonicalUrl"`
	Title        string `json:"title"`
	Selection    string `json:"selection"`
	ContentText  string `json:"contentText"`
	Unread       bool   `json:"unread"`
	Starred      bool   `json:"starred"`
}

func (a *App) handleExtensionPairingCreate(w http.ResponseWriter, r *http.Request) {
	code, expiresAt, err := a.store.CreateExtensionPairing(r.Context(), 10*time.Minute)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"code": code, "expiresAt": expiresAt})
}

func (a *App) handleExtensionPair(w http.ResponseWriter, r *http.Request) {
	var input extensionPairRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.DeviceName) > 100 {
		writeError(w, http.StatusBadRequest, "设备名称过长")
		return
	}
	token, err := a.store.RedeemExtensionPairing(r.Context(), input.Code, input.DeviceName)
	if isNotFound(err) {
		writeError(w, http.StatusUnauthorized, "配对码无效或已过期")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": token})
}

func (a *App) handleExtensionClientsList(w http.ResponseWriter, r *http.Request) {
	clients, err := a.store.ListExtensionClients(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": clients})
}

func (a *App) handleExtensionClientRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "扩展设备 ID 无效")
		return
	}
	if err := a.store.RevokeExtensionClient(r.Context(), id); isNotFound(err) {
		writeError(w, http.StatusNotFound, "扩展设备不存在")
		return
	} else if err != nil {
		a.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleCapture(w http.ResponseWriter, r *http.Request) {
	authorization := r.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "缺少扩展访问凭据")
		return
	}
	if err := a.store.ValidateCaptureToken(r.Context(), strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))); err != nil {
		writeError(w, http.StatusUnauthorized, "扩展访问凭据无效或已撤销")
		return
	}
	var input captureRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Title) > 500 || len(input.Selection) > 10000 || len(input.ContentText) > 2<<20 {
		writeError(w, http.StatusBadRequest, "标题或选中文字过长")
		return
	}
	original, canonical, err := bookmarks.NormalizeURL(input.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(input.CanonicalURL) != "" {
		_, canonical, err = bookmarks.NormalizeURL(input.CanonicalURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, "页面 canonical URL 无效")
			return
		}
	}
	created, duplicate, err := a.store.CreateBookmark(r.Context(), store.Bookmark{
		URL: original, CanonicalURL: canonical,
		Title: strings.TrimSpace(input.Title), Note: strings.TrimSpace(input.Selection),
		Unread: input.Unread, Starred: input.Starred, CaptureSource: "chromium",
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if strings.TrimSpace(input.ContentText) != "" {
		if err := a.archiver.StoreClientText(r.Context(), created.ID, created.Title, input.ContentText); err != nil {
			a.logger.Warn("client-side archive failed", "bookmark_id", created.ID, "error", err)
		}
		created, _ = a.store.GetBookmark(r.Context(), created.ID)
	}
	status := http.StatusAccepted
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"bookmark": created, "duplicate": duplicate})
}
