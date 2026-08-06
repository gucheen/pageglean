package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"links/internal/archive"
	"links/internal/config"
	"links/internal/store"
	"links/internal/webui"
)

type App struct {
	cfg      config.Config
	store    *store.Store
	webauthn *webauthn.WebAuthn
	archiver *archive.Archiver
	logger   *slog.Logger
	handler  http.Handler
}

const webAuthnCeremonyTimeout = 5 * time.Minute

func New(cfg config.Config, data *store.Store, logger *slog.Logger) (*App, error) {
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "Links",
		RPID:          cfg.RPID,
		RPOrigins:     []string{cfg.PublicOrigin},
		Timeouts: webauthn.TimeoutsConfig{
			Login: webauthn.TimeoutConfig{
				Enforce: true, Timeout: webAuthnCeremonyTimeout, TimeoutUVD: webAuthnCeremonyTimeout,
			},
			Registration: webauthn.TimeoutConfig{
				Enforce: true, Timeout: webAuthnCeremonyTimeout, TimeoutUVD: webAuthnCeremonyTimeout,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("configure WebAuthn: %w", err)
	}
	if logger == nil {
		logger = slog.Default()
	}
	a := &App{cfg: cfg, store: data, webauthn: wa, logger: logger}
	a.archiver = archive.New(cfg, data, logger)
	a.handler = a.routes()
	return a, nil
}

func (a *App) Handler() http.Handler { return a.handler }

func (a *App) Start(ctx context.Context) {
	go a.archiver.Run(ctx)
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.handleHealth)
	mux.HandleFunc("GET /api/status", a.handleStatus)
	mux.HandleFunc("POST /api/auth/register/start", a.handleRegisterStart)
	mux.HandleFunc("POST /api/auth/register/finish", a.handleRegisterFinish)
	mux.HandleFunc("POST /api/auth/login/start", a.handleLoginStart)
	mux.HandleFunc("POST /api/auth/login/finish", a.handleLoginFinish)
	mux.HandleFunc("POST /api/auth/logout", a.handleLogout)
	mux.Handle("POST /api/extension/pairings", a.requireAuth(http.HandlerFunc(a.handleExtensionPairingCreate)))
	mux.Handle("GET /api/extension/clients", a.requireAuth(http.HandlerFunc(a.handleExtensionClientsList)))
	mux.Handle("DELETE /api/extension/clients/{id}", a.requireAuth(http.HandlerFunc(a.handleExtensionClientRevoke)))
	mux.HandleFunc("POST /api/extension/pair", a.handleExtensionPair)
	mux.HandleFunc("POST /api/capture", a.handleCapture)
	mux.Handle("GET /api/bookmarks", a.requireAuth(http.HandlerFunc(a.handleBookmarksList)))
	mux.Handle("POST /api/bookmarks", a.requireAuth(http.HandlerFunc(a.handleBookmarksCreate)))
	mux.Handle("PATCH /api/bookmarks/{id}", a.requireAuth(http.HandlerFunc(a.handleBookmarksUpdate)))
	mux.Handle("DELETE /api/bookmarks/{id}", a.requireAuth(http.HandlerFunc(a.handleBookmarksDelete)))
	mux.Handle("POST /api/bookmarks/{id}/archive/retry", a.requireAuth(http.HandlerFunc(a.handleArchiveRetry)))
	mux.Handle("GET /archive/{id}", a.requireAuth(http.HandlerFunc(a.handleArchiveRead)))
	mux.Handle("GET /api/export", a.requireAuth(http.HandlerFunc(a.handleExport)))
	mux.Handle("GET /api/stats", a.requireAuth(http.HandlerFunc(a.handleStats)))

	assets, err := fs.Sub(webui.Assets, "assets")
	if err != nil {
		panic(err)
	}
	static := http.FileServer(http.FS(assets))
	mux.Handle("GET /", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" && r.URL.Path != "/index.html" && !strings.Contains(r.URL.Path, ".") {
			r.URL.Path = "/"
		}
		static.ServeHTTP(w, r)
	}))
	return a.securityHeaders(a.extensionCORS(a.originCheck(mux)))
}

func (a *App) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (a *App) originCheck(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" && origin != a.cfg.PublicOrigin && !isAllowedExtensionRequest(r, origin) {
				writeError(w, http.StatusForbidden, "请求来源无效")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) extensionCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if isAllowedExtensionRequest(r, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isAllowedExtensionRequest(r *http.Request, origin string) bool {
	if !strings.HasPrefix(origin, "chrome-extension://") {
		return false
	}
	return r.URL.Path == "/api/extension/pair" || r.URL.Path == "/api/capture"
}

type contextKey string

const userIDKey contextKey = "userID"

func (a *App) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := a.currentUser(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "需要使用 Passkey 登录")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, userID)))
	})
}

func (a *App) currentUser(r *http.Request) (int64, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return 0, false
	}
	userID, err := a.store.ValidateAppSession(r.Context(), cookie.Value)
	return userID, err == nil
}

func (a *App) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	count, err := a.store.CredentialCount(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	_, authenticated := a.currentUser(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"setupRequired": count == 0,
		"authenticated": authenticated,
	})
}

func (a *App) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, "服务器处理请求失败")
}

func (a *App) Shutdown(_ context.Context) error { return nil }

func sessionExpiry() time.Duration { return 30 * 24 * time.Hour }

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
