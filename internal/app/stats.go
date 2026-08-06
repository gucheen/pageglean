package app

import (
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
)

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := a.store.Stats(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	if info, err := os.Stat(a.cfg.DatabasePath); err == nil {
		stats.DatabaseBytes = info.Size()
	}
	blobs := filepath.Join(a.cfg.DataDir, "blobs")
	_ = filepath.WalkDir(blobs, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		if info, infoErr := entry.Info(); infoErr == nil {
			stats.ArchiveBytes += info.Size()
		}
		return nil
	})
	writeJSON(w, http.StatusOK, stats)
}
