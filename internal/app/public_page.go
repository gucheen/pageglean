package app

import (
	"bytes"
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"pageglean/internal/store"
	"pageglean/internal/webui"
)

var publicPageTemplate = template.Must(template.New("public.html").Funcs(template.FuncMap{
	"domain": func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return ""
		}
		return u.Hostname()
	},
}).ParseFS(webui.Templates, "templates/public.html"))

func (a *App) handlePublicBookmarks(w http.ResponseWriter, r *http.Request) {
	const pageSize = 30
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed < 1 {
			http.Error(w, "页码无效", http.StatusBadRequest)
			return
		}
		page = int(parsed)
	}
	items, err := a.store.PublicBookmarks(r.Context(), pageSize+1, (page-1)*pageSize)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if page > 1 && len(items) == 0 {
		http.NotFound(w, r)
		return
	}
	view := struct {
		Bookmarks            []store.PublicBookmark
		Page, Previous, Next int
	}{Bookmarks: items, Page: page}
	if page > 1 {
		view.Previous = page - 1
	}
	if len(items) > pageSize {
		view.Bookmarks = items[:pageSize]
		view.Next = page + 1
	}
	var body bytes.Buffer
	if err := publicPageTemplate.Execute(&body, view); err != nil {
		a.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(body.Bytes())
}
