package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookieName = "pageglean_session"
	loginCookieName   = "pageglean_oidc"
	loginTTL          = 5 * time.Minute
)

type oidcLogin struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	ReturnTo string `json:"returnTo"`
	Identity string `json:"identity"`
}

func (a *App) authIdentity() string {
	encoded, _ := json.Marshal([]string{a.cfg.OIDCIssuer, a.cfg.OIDCClientID, a.cfg.OIDCOwnerSubject})
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func safeReturnTo(value string) string {
	u, err := url.Parse(value)
	if err != nil || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n") || u.IsAbs() || u.Host != "" || u.Path == "" || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\r\n") || strings.HasPrefix(u.Path, "/api/") {
		return "/"
	}
	return u.String()
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	login := oidcLogin{State: oauth2.GenerateVerifier(), Nonce: oauth2.GenerateVerifier(), Verifier: oauth2.GenerateVerifier(), ReturnTo: safeReturnTo(r.URL.Query().Get("return_to")), Identity: a.authIdentity()}
	encoded, err := json.Marshal(login)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	// Replacing a browser's pending login invalidates its previous callback.
	if cookie, err := r.Cookie(loginCookieName); err == nil {
		_, _ = a.store.TakeOIDCLogin(r.Context(), cookie.Value)
	}
	token, err := a.store.CreateOIDCLogin(r.Context(), encoded, loginTTL)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.loginCookie(w, token, int(loginTTL.Seconds()))
	http.Redirect(w, r, a.oauthConfig.AuthCodeURL(login.State, oidc.Nonce(login.Nonce), oauth2.S256ChallengeOption(login.Verifier), oauth2.SetAuthURLParam("response_mode", "query")), http.StatusFound)
}

func (a *App) handleCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	cookie, err := r.Cookie(loginCookieName)
	if err != nil {
		writeError(w, http.StatusBadRequest, "登录请求已过期，请重新登录")
		return
	}
	encoded, err := a.store.TakeOIDCLogin(r.Context(), cookie.Value)
	a.loginCookie(w, "", -1)
	var login oidcLogin
	if err != nil || json.Unmarshal(encoded, &login) != nil || login.Identity != a.authIdentity() || login.State == "" || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(login.State)) != 1 {
		writeError(w, http.StatusBadRequest, "登录请求无效或已过期，请重新登录")
		return
	}
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		writeError(w, http.StatusUnauthorized, "身份服务未完成登录，请重新登录")
		return
	}
	ctx, cancel := context.WithTimeout(oidc.ClientContext(r.Context(), a.authClient), 20*time.Second)
	defer cancel()
	token, err := a.oauthConfig.Exchange(ctx, r.URL.Query().Get("code"), oauth2.VerifierOption(login.Verifier))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "无法完成身份验证，请重新登录")
		return
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		writeError(w, http.StatusUnauthorized, "身份服务未返回有效的身份令牌")
		return
	}
	idToken, err := a.oidcVerifier.Verify(ctx, rawIDToken)
	if err != nil || idToken.Nonce == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(login.Nonce)) != 1 {
		writeError(w, http.StatusUnauthorized, "身份令牌验证失败，请重新登录")
		return
	}
	if idToken.Subject == "" || idToken.Subject != a.cfg.OIDCOwnerSubject {
		writeError(w, http.StatusForbidden, "此账号无权访问拾页")
		return
	}
	if idToken.AccessTokenHash != "" {
		if err := idToken.VerifyAccessToken(token.AccessToken); err != nil {
			writeError(w, http.StatusUnauthorized, "身份令牌验证失败，请重新登录")
			return
		}
	}
	if err := a.startAppSession(w, r, 1); err != nil {
		a.internalError(w, r, err)
		return
	}
	http.Redirect(w, r, safeReturnTo(login.ReturnTo), http.StatusSeeOther)
}

func (a *App) loginCookie(w http.ResponseWriter, token string, maxAge int) {
	cookie := &http.Cookie{Name: loginCookieName, Value: token, Path: "/api/auth/", MaxAge: maxAge, HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteLaxMode}
	if maxAge > 0 {
		cookie.Expires = time.Now().Add(loginTTL)
	}
	http.SetCookie(w, cookie)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := a.store.DeleteAppSession(r.Context(), cookie.Value); err != nil {
			a.internalError(w, r, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) startAppSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		if err := a.store.DeleteAppSession(r.Context(), cookie.Value); err != nil {
			return err
		}
	}
	token, err := a.store.CreateAppSession(r.Context(), userID, sessionExpiry(), a.authIdentity())
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", Expires: time.Now().Add(sessionExpiry()), MaxAge: int(sessionExpiry().Seconds()), HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode})
	return nil
}
