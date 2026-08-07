package app

import (
	"net/http"
	"strconv"
	"strings"

	"pageglean/internal/bookmarks"
	"pageglean/internal/store"
)

type createBookmarkRequest struct {
	URL     string   `json:"url"`
	Title   string   `json:"title"`
	Note    string   `json:"note"`
	Tags    []string `json:"tags"`
	Unread  bool     `json:"unread"`
	Starred bool     `json:"starred"`
}

type updateBookmarkRequest struct {
	Title   *string   `json:"title"`
	Note    *string   `json:"note"`
	Tags    *[]string `json:"tags"`
	Unread  *bool     `json:"unread"`
	Starred *bool     `json:"starred"`
}

type bulkBookmarkRequest struct {
	IDs        []int64  `json:"ids"`
	AddTags    []string `json:"addTags"`
	RemoveTags []string `json:"removeTags"`
	Unread     *bool    `json:"unread"`
	Starred    *bool    `json:"starred"`
}

func (a *App) handleBookmarksCreate(w http.ResponseWriter, r *http.Request) {
	var input createBookmarkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if len(input.Title) > 500 || len(input.Note) > 10000 {
		writeError(w, http.StatusBadRequest, "标题或备注过长")
		return
	}
	original, canonical, err := bookmarks.NormalizeURL(input.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, duplicate, err := a.store.CreateBookmark(r.Context(), store.Bookmark{
		URL:          original,
		CanonicalURL: canonical,
		Title:        strings.TrimSpace(input.Title),
		Note:         strings.TrimSpace(input.Note),
		Tags:         input.Tags,
		Unread:       input.Unread,
		Starred:      input.Starred,
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	status := http.StatusCreated
	if duplicate {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"bookmark": created, "duplicate": duplicate})
}

func (a *App) handleBookmarksList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	items, err := a.store.ListBookmarks(r.Context(), store.BookmarkFilter{
		Query: r.URL.Query().Get("q"),
		State: r.URL.Query().Get("state"),
		Limit: limit, Offset: offset,
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookmarks": items})
}

func (a *App) handleBookmarksUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "书签 ID 无效")
		return
	}
	var input updateBookmarkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	bookmark, err := a.store.GetBookmark(r.Context(), id)
	if isNotFound(err) {
		writeError(w, http.StatusNotFound, "书签不存在")
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if input.Title != nil {
		if len(*input.Title) > 500 {
			writeError(w, http.StatusBadRequest, "标题过长")
			return
		}
		bookmark.Title = strings.TrimSpace(*input.Title)
	}
	if input.Note != nil {
		if len(*input.Note) > 10000 {
			writeError(w, http.StatusBadRequest, "备注过长")
			return
		}
		bookmark.Note = strings.TrimSpace(*input.Note)
	}
	if input.Tags != nil {
		bookmark.Tags = *input.Tags
	}
	if input.Unread != nil {
		bookmark.Unread = *input.Unread
	}
	if input.Starred != nil {
		bookmark.Starred = *input.Starred
	}
	updated, err := a.store.UpdateBookmark(r.Context(), bookmark)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"bookmark": updated})
}

func (a *App) handleBookmarksDelete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "书签 ID 无效")
		return
	}
	if err := a.store.DeleteBookmark(r.Context(), id); isNotFound(err) {
		writeError(w, http.StatusNotFound, "书签不存在")
		return
	} else if err != nil {
		a.internalError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleBookmarksBulkUpdate(w http.ResponseWriter, r *http.Request) {
	var input bulkBookmarkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if !validateBulkRequest(w, input) {
		return
	}
	if len(input.AddTags) == 0 && len(input.RemoveTags) == 0 && input.Unread == nil && input.Starred == nil {
		writeError(w, http.StatusBadRequest, "没有需要批量修改的字段")
		return
	}
	updated, err := a.store.BulkUpdateBookmarks(r.Context(), input.IDs, store.BulkBookmarkPatch{
		AddTags: input.AddTags, RemoveTags: input.RemoveTags, Unread: input.Unread, Starred: input.Starred,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"updated": updated})
}

func (a *App) handleBookmarksBulkDelete(w http.ResponseWriter, r *http.Request) {
	var input bulkBookmarkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if !validateBulkRequest(w, input) {
		return
	}
	deleted, err := a.store.BulkDeleteBookmarks(r.Context(), input.IDs)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"deleted": deleted})
}

func validateBulkRequest(w http.ResponseWriter, input bulkBookmarkRequest) bool {
	if len(input.IDs) == 0 || len(input.IDs) > 500 {
		writeError(w, http.StatusBadRequest, "每次请选择 1 至 500 条书签")
		return false
	}
	if len(input.AddTags)+len(input.RemoveTags) > 100 {
		writeError(w, http.StatusBadRequest, "批量标签数量过多")
		return false
	}
	for _, tag := range append(append([]string{}, input.AddTags...), input.RemoveTags...) {
		if len([]rune(strings.TrimSpace(tag))) > 50 {
			writeError(w, http.StatusBadRequest, "标签不能超过 50 个字符")
			return false
		}
	}
	return true
}
