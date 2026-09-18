package app

import (
	"bytes"
	"html/template"
	"net/http"
	"pageglean/internal/webui"
)

var indexTemplate = template.Must(template.ParseFS(webui.Assets, "assets/index.html"))

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	_, authenticated := a.currentUser(r)
	setupRequired := false
	if !authenticated {
		count, err := a.store.CredentialCount(r.Context())
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		setupRequired = count == 0
	}
	capture := r.URL.Path == "/add"
	view := struct{ Authenticated, SetupRequired, ShowApp, ShowCapture, ShowAuth bool }{
		Authenticated: authenticated,
		SetupRequired: setupRequired,
		ShowApp:       authenticated && !capture,
		ShowCapture:   capture,
		ShowAuth:      !authenticated && !capture,
	}
	var body bytes.Buffer
	if err := indexTemplate.Execute(&body, view); err != nil {
		a.internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(body.Bytes())
}
