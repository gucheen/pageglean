package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"pageglean/internal/bookmarks"
	"pageglean/internal/importer"
	"pageglean/internal/store"
)

const maxImportRequestBytes = importer.MaxFileBytes + (1 << 20)

func (a *App) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	result, ok := parseImportRequest(w, r)
	if !ok {
		return
	}
	prepared, invalid := prepareImportedItems(result.Items, false)
	preview := prepared
	if len(preview) > 10 {
		preview = preview[:10]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"format": result.Format, "headers": result.Headers, "mapping": result.Mapping,
		"rows": result.Rows, "importable": len(prepared), "invalid": result.Skipped + invalid,
		"preview": preview,
	})
}

func (a *App) handleImportCommit(w http.ResponseWriter, r *http.Request) {
	result, ok := parseImportRequest(w, r)
	if !ok {
		return
	}
	if result.Format == "csv" && result.Mapping.URL == "" {
		writeError(w, http.StatusBadRequest, "请选择 CSV 中的网址列")
		return
	}
	archive := strings.EqualFold(strings.TrimSpace(r.FormValue("archive")), "true")
	prepared, invalid := prepareImportedItems(result.Items, !archive)
	created := 0
	duplicates := 0
	for _, bookmark := range prepared {
		_, duplicate, err := a.store.CreateBookmark(r.Context(), bookmark)
		if err != nil {
			a.internalError(w, r, fmt.Errorf("import bookmark %q: %w", bookmark.URL, err))
			return
		}
		if duplicate {
			duplicates++
		} else {
			created++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{
		"created": created, "duplicates": duplicates, "invalid": result.Skipped + invalid,
	})
}

func parseImportRequest(w http.ResponseWriter, r *http.Request) (importer.Result, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportRequestBytes)
	if err := r.ParseMultipartForm(importer.MaxFileBytes); err != nil {
		writeError(w, http.StatusBadRequest, "无法读取导入文件，文件不能超过 10 MB")
		return importer.Result{}, false
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "请选择要导入的文件")
		return importer.Result{}, false
	}
	defer file.Close()
	var mapping importer.Mapping
	if raw := strings.TrimSpace(r.FormValue("mapping")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &mapping); err != nil {
			writeError(w, http.StatusBadRequest, "CSV 字段映射无效")
			return importer.Result{}, false
		}
	}
	result, err := importer.Parse(header.Filename, file, mapping)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return importer.Result{}, false
	}
	return result, true
}

func prepareImportedItems(items []importer.Item, skipArchive bool) ([]store.Bookmark, int) {
	prepared := make([]store.Bookmark, 0, len(items))
	invalid := 0
	for _, item := range items {
		original, canonical, err := bookmarks.NormalizeURL(item.URL)
		if err != nil || len(item.Title) > 500 || len(item.Note) > 10_000 || !validImportTags(item.Tags) {
			invalid++
			continue
		}
		prepared = append(prepared, store.Bookmark{
			URL: original, CanonicalURL: canonical, Title: strings.TrimSpace(item.Title),
			Note: strings.TrimSpace(item.Note), Tags: item.Tags, Unread: item.Unread, Starred: item.Starred,
			CreatedAt: item.CreatedAt, CaptureSource: "import", SkipArchive: skipArchive,
		})
	}
	return prepared, invalid
}

func validImportTags(tags []string) bool {
	if len(tags) > 50 {
		return false
	}
	for _, tag := range tags {
		if len([]rune(strings.TrimSpace(tag))) > 50 {
			return false
		}
	}
	return true
}
