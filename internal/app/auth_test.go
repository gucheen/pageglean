package app

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"golang.org/x/oauth2"
)

func newTestProvider(t *testing.T) *httptest.Server {
	return testProvider(t, nil, nil)
}

func testProvider(t *testing.T, keys *jose.JSONWebKeySet, tokenHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, keys) })
	if tokenHandler != nil {
		mux.HandleFunc("POST /token", tokenHandler)
	}
	return server
}

func TestOIDCLoginAndCallback(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"success", "wrong subject", "missing subject", "wrong nonce", "missing nonce", "wrong issuer", "wrong audience", "expired", "bad signature", "missing token", "token error", "wrong state", "no cookie", "provider denied"} {
		t.Run(name, func(t *testing.T) {
			a, data := newTestApp(t)
			var authorization url.Values
			exchanges := 0
			var issuer string
			provider := testProvider(t, &jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}}, func(w http.ResponseWriter, r *http.Request) {
				exchanges++
				if err := r.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if r.Form.Get("code") != "code" || r.Form.Get("redirect_uri") != a.cfg.PublicURL+"/api/auth/callback" {
					t.Error("incorrect token request")
				}
				if r.Form.Get("code_verifier") == "" {
					t.Error("missing PKCE verifier")
				}
				check := (&oauth2.Config{ClientID: "pageglean"}).AuthCodeURL("", oauth2.S256ChallengeOption(r.Form.Get("code_verifier")))
				u, _ := url.Parse(check)
				if u.Query().Get("code_challenge") != authorization.Get("code_challenge") {
					t.Error("incorrect PKCE verifier")
				}
				if name == "token error" {
					http.Error(w, "secret provider error", 500)
					return
				}
				if name == "missing token" {
					writeJSON(w, 200, map[string]string{"access_token": "access", "token_type": "Bearer"})
					return
				}
				claims := map[string]any{"iss": issuer, "aud": "pageglean", "sub": "owner", "nonce": authorization.Get("nonce"), "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
				switch name {
				case "wrong subject":
					claims["sub"] = "other"
				case "missing subject":
					delete(claims, "sub")
				case "wrong nonce":
					claims["nonce"] = "other"
				case "missing nonce":
					delete(claims, "nonce")
				case "wrong issuer":
					claims["iss"] = "https://other.example"
				case "wrong audience":
					claims["aud"] = "other"
				case "expired":
					claims["exp"] = time.Now().Add(-time.Hour).Unix()
				}
				encoded, _ := json.Marshal(claims)
				signingKey := signer
				if name == "bad signature" {
					otherKey, _ := rsa.GenerateKey(rand.Reader, 2048)
					signingKey, _ = jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: otherKey}, nil)
				}
				signed, err := signingKey.Sign(encoded)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := signed.CompactSerialize()
				writeJSON(w, 200, map[string]string{"access_token": "access", "token_type": "Bearer", "id_token": raw})
			})
			issuer = provider.URL
			a.cfg.OIDCIssuer = issuer
			a, err = New(a.cfg, data, a.logger)
			if err != nil {
				t.Fatal(err)
			}
			old, err := data.CreateAppSession(t.Context(), 1, time.Hour, a.authIdentity())
			if err != nil {
				t.Fatal(err)
			}
			returnTo := "/add?url=https%3A%2F%2Fexample.com#title=待保存"
			start := httptest.NewRecorder()
			a.Handler().ServeHTTP(start, httptest.NewRequest("GET", "/api/auth/login?"+url.Values{"return_to": {returnTo}}.Encode(), nil))
			if start.Code != 302 {
				t.Fatalf("login: %d %s", start.Code, start.Body)
			}
			destination, _ := url.Parse(start.Header().Get("Location"))
			authorization = destination.Query()
			if authorization.Get("state") == "" || authorization.Get("nonce") == "" || authorization.Get("code_challenge_method") != "S256" || authorization.Get("scope") != "openid" {
				t.Fatalf("authorization: %v", authorization)
			}
			cookie := start.Result().Cookies()[0]
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 300 {
				t.Fatal("incorrect login cookie")
			}
			query := url.Values{"state": {authorization.Get("state")}, "code": {"code"}}
			if name == "wrong state" {
				query.Set("state", "wrong")
			}
			if name == "provider denied" {
				query.Set("error", "access_denied")
			}
			request := httptest.NewRequest("GET", "/api/auth/callback?"+query.Encode(), nil)
			if name != "no cookie" {
				request.AddCookie(cookie)
			}
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: old})
			response := httptest.NewRecorder()
			a.Handler().ServeHTTP(response, request)
			if name == "success" {
				if response.Code != 303 || response.Header().Get("Location") != safeReturnTo(returnTo) {
					t.Fatalf("callback: %d %s %s", response.Code, response.Header().Get("Location"), response.Body)
				}
				authenticated := false
				for _, c := range response.Result().Cookies() {
					if c.Name == sessionCookieName {
						req := httptest.NewRequest("GET", "/", nil)
						req.AddCookie(c)
						_, authenticated = a.currentUser(req)
						if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
							t.Fatal("incorrect session cookie")
						}
					}
				}
				if !authenticated {
					t.Fatal("callback did not create authenticated session")
				}
				if _, err := data.ValidateAppSession(t.Context(), old, a.authIdentity()); err == nil {
					t.Fatal("previous session remained valid")
				}
			} else {
				if response.Code < 400 {
					t.Fatalf("accepted %s: %d", name, response.Code)
				}
				for _, c := range response.Result().Cookies() {
					if c.Name == sessionCookieName && c.Value != "" {
						t.Fatal("invalid login created a session")
					}
				}
				if strings.Contains(response.Body.String(), "secret provider error") {
					t.Fatal("provider error leaked")
				}
			}
			if name != "no cookie" {
				replay := httptest.NewRecorder()
				a.Handler().ServeHTTP(replay, request)
				if replay.Code != 400 {
					t.Fatalf("replay: %d", replay.Code)
				}
			}
			if (name == "wrong state" || name == "no cookie" || name == "provider denied") && exchanges != 0 {
				t.Fatal("invalid callback contacted token endpoint")
			}
		})
	}
}

func TestSafeReturnTo(t *testing.T) {
	for _, value := range []string{"https://evil.example", "//evil.example", "/\\evil.example", "/%2F%2Fevil.example", "/api/auth/login", "/\r\nevil"} {
		if got := safeReturnTo(value); got != "/" {
			t.Errorf("accepted %q as %q", value, got)
		}
	}
}

func TestOIDCIdentityChangeInvalidatesSession(t *testing.T) {
	a, data := newTestApp(t)
	token, err := data.CreateAppSession(t.Context(), 1, time.Hour, a.authIdentity())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/status", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	a.cfg.OIDCOwnerSubject = "other"
	if _, ok := a.currentUser(req); ok {
		t.Fatal("session survived owner change")
	}
}

func TestPasskeyRoutesRemoved(t *testing.T) {
	a, _ := newTestApp(t)
	for _, path := range []string{"/api/auth/register/start", "/api/auth/register/finish", "/api/auth/login/start", "/api/auth/login/finish"} {
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code < 400 {
			t.Fatalf("old route is active: %s", path)
		}
	}
}
