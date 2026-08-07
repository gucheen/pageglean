package app

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
)

func (a *App) handleArchiveRetry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "书签 ID 无效")
		return
	}
	if err := a.store.RetryArchive(r.Context(), id); isNotFound(err) {
		writeError(w, http.StatusNotFound, "书签不存在")
		return
	} else if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func (a *App) handleArchiveRead(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "书签 ID 无效", http.StatusBadRequest)
		return
	}
	bookmark, err := a.store.GetBookmark(r.Context(), id)
	if isNotFound(err) {
		http.Error(w, "书签不存在", http.StatusNotFound)
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if bookmark.ArchiveStatus != "complete" || bookmark.ContentPath == "" {
		http.Error(w, "正文归档尚未完成", http.StatusConflict)
		return
	}
	fragment, err := a.archiver.Read(bookmark.ContentPath)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s · 拾页</title><link rel="stylesheet" href="/reader.css"></head>
<body><header class="reader-header"><a href="/">← 返回拾页</a><a href="%s" target="_blank" rel="noopener noreferrer">打开原网页 ↗</a></header><main>`,
		html.EscapeString(bookmark.Title), html.EscapeString(bookmark.URL))
	_, _ = w.Write(fragment)
	_, _ = w.Write([]byte(`</main></body></html>`))
}
