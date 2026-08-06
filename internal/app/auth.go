package app

import (
	"bytes"
	"errors"
	"net/http"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"links/internal/store"
)

const (
	sessionCookieName  = "links_session"
	ceremonyCookieName = "links_webauthn"
)

type registrationStartRequest struct {
	Token string `json:"token"`
	Label string `json:"label"`
}

func (a *App) handleRegisterStart(w http.ResponseWriter, r *http.Request) {
	var input registrationStartRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	count, err := a.store.CredentialCount(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}

	var adminHash []byte
	if input.Token != "" {
		var kind string
		adminHash, kind, err = a.store.ValidateAdminToken(r.Context(), input.Token)
		if err != nil || (kind == "setup" && count != 0) || (kind == "recovery" && count == 0) {
			writeError(w, http.StatusUnauthorized, "初始化或恢复链接无效或已过期")
			return
		}
	} else if _, ok := a.currentUser(r); !ok {
		writeError(w, http.StatusUnauthorized, "需要有效的初始化、恢复链接或登录会话")
		return
	}

	user, err := a.store.LoadOwner(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	creation, session, err := a.webauthn.BeginRegistration(
		user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(webauthn.Credentials(user.Credentials).CredentialDescriptors()),
	)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	ceremonyToken, err := a.store.CreateCeremony(r.Context(), "registration", session, adminHash)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.setCeremonyCookie(w, ceremonyToken, session.Expires)
	writeJSON(w, http.StatusOK, creation)
}

func (a *App) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(ceremonyCookieName)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Passkey 注册会话已过期")
		return
	}
	session, adminHash, err := a.store.TakeCeremony(r.Context(), cookie.Value, "registration")
	a.clearCeremonyCookie(w)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Passkey 注册会话已过期")
		return
	}
	user, err := a.store.LoadOwner(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	credential, err := a.webauthn.FinishRegistration(user, *session, r)
	if err != nil {
		a.logger.Warn("Passkey registration rejected", "error", err)
		writeError(w, http.StatusBadRequest, "无法验证 Passkey 注册结果")
		return
	}
	if len(adminHash) > 0 {
		if err := a.store.ConsumeAdminToken(r.Context(), adminHash); err != nil {
			writeError(w, http.StatusUnauthorized, "初始化或恢复链接已经失效")
			return
		}
	}
	if err := a.store.AddCredential(r.Context(), user.ID, a.cfg.RPID, "Passkey", credential); err != nil {
		a.internalError(w, r, err)
		return
	}
	if err := a.startAppSession(w, r, user.ID); err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (a *App) handleLoginStart(w http.ResponseWriter, r *http.Request) {
	count, err := a.store.CredentialCount(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if count == 0 {
		writeError(w, http.StatusConflict, "应用尚未完成初始化")
		return
	}
	assertion, session, err := a.webauthn.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired),
	)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	ceremonyToken, err := a.store.CreateCeremony(r.Context(), "login", session, nil)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.setCeremonyCookie(w, ceremonyToken, session.Expires)
	writeJSON(w, http.StatusOK, assertion)
}

func (a *App) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(ceremonyCookieName)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Passkey 登录会话已过期")
		return
	}
	session, _, err := a.store.TakeCeremony(r.Context(), cookie.Value, "login")
	a.clearCeremonyCookie(w)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Passkey 登录会话已过期")
		return
	}
	validatedUser, credential, err := a.webauthn.FinishPasskeyLogin(
		func(rawID, userHandle []byte) (webauthn.User, error) {
			user, err := a.store.LoadUserByHandle(r.Context(), userHandle)
			if err != nil {
				return nil, err
			}
			for _, saved := range user.Credentials {
				if bytes.Equal(saved.ID, rawID) {
					return user, nil
				}
			}
			return nil, store.ErrNotFound
		},
		*session,
		r,
	)
	if err != nil {
		a.logger.Warn("Passkey login rejected", "error", err)
		writeError(w, http.StatusUnauthorized, "无法验证 Passkey")
		return
	}
	user, ok := validatedUser.(*store.User)
	if !ok {
		a.internalError(w, r, errors.New("unexpected WebAuthn user type"))
		return
	}
	if err := a.store.UpdateCredential(r.Context(), credential); err != nil {
		a.internalError(w, r, err)
		return
	}
	if err := a.startAppSession(w, r, user.ID); err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = a.store.DeleteAppSession(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *App) startAppSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, err := a.store.CreateAppSession(r.Context(), userID, sessionExpiry())
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/",
		Expires: a.storeTime().Add(sessionExpiry()), MaxAge: int(sessionExpiry().Seconds()),
		HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
	return nil
}

func (a *App) setCeremonyCookie(w http.ResponseWriter, token string, expires time.Time) {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge <= 0 {
		maxAge = 300
	}
	http.SetCookie(w, &http.Cookie{
		Name: ceremonyCookieName, Value: token, Path: "/api/auth/",
		Expires: expires, MaxAge: maxAge,
		HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func (a *App) clearCeremonyCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: ceremonyCookieName, Value: "", Path: "/api/auth/", MaxAge: -1,
		HttpOnly: true, Secure: a.cfg.SecureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func (a *App) storeTime() time.Time { return time.Now().UTC() }
