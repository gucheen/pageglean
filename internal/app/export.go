package app

import (
	"encoding/csv"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pageglean/internal/store"
)

func (a *App) handleExport(w http.ResponseWriter, r *http.Request) {
	bookmarks, err := a.allBookmarks(r)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	date := time.Now().UTC().Format("20060102")
	switch format {
	case "", "json":
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pageglean-%s.json"`, date))
		writeJSON(w, http.StatusOK, map[string]any{
			"version": 1, "exportedAt": time.Now().UTC(), "bookmarks": bookmarks,
		})
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pageglean-%s.csv"`, date))
		_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
		writer := csv.NewWriter(w)
		_ = writer.Write([]string{"url", "title", "note", "tags", "unread", "starred", "created_at", "archive_status"})
		for _, bookmark := range bookmarks {
			_ = writer.Write([]string{
				bookmark.URL, bookmark.Title, bookmark.Note, strings.Join(bookmark.Tags, ","),
				strconv.FormatBool(bookmark.Unread), strconv.FormatBool(bookmark.Starred),
				bookmark.CreatedAt.Format(time.RFC3339), bookmark.ArchiveStatus,
			})
		}
		writer.Flush()
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="pageglean-%s.html"`, date))
		_, _ = fmt.Fprintln(w, `<!DOCTYPE NETSCAPE-Bookmark-file-1>`)
		_, _ = fmt.Fprintln(w, `<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">`)
		_, _ = fmt.Fprintln(w, `<TITLE>拾页导出</TITLE><H1>拾页导出</H1><DL><p>`)
		for _, bookmark := range bookmarks {
			title := bookmark.Title
			if title == "" {
				title = bookmark.URL
			}
			_, _ = fmt.Fprintf(w, `<DT><A HREF="%s" ADD_DATE="%d" TAGS="%s">%s</A>`+"\n",
				html.EscapeString(bookmark.URL), bookmark.CreatedAt.Unix(),
				html.EscapeString(strings.Join(bookmark.Tags, ",")), html.EscapeString(title))
			if bookmark.Note != "" {
				_, _ = fmt.Fprintf(w, "<DD>%s\n", html.EscapeString(bookmark.Note))
			}
		}
		_, _ = fmt.Fprintln(w, `</DL><p>`)
	default:
		writeError(w, http.StatusBadRequest, "导出格式只支持 json、csv 或 html")
	}
}

func (a *App) allBookmarks(r *http.Request) ([]store.Bookmark, error) {
	all := []store.Bookmark{}
	for offset := 0; ; offset += 200 {
		items, err := a.store.ListBookmarks(r.Context(), store.BookmarkFilter{Limit: 200, Offset: offset})
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < 200 {
			return all, nil
		}
	}
}
