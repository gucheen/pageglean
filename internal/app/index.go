package app

import (
	"bytes"
	"html/template"
	"net/http"
	"pageglean/internal/webui"
)

var indexTemplate = template.Must(template.New("index.html").Funcs(template.FuncMap{
	"assetURL": webui.AssetURL,
}).ParseFS(webui.Assets, "assets/index.html"))

func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	_, authenticated := a.currentUser(r)
	capture := r.URL.Path == "/add"
	view := struct{ Authenticated, ShowApp, ShowCapture, ShowAuth bool }{
		Authenticated: authenticated,
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
