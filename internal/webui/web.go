package webui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Assets contains the browser application.
//
//go:embed assets/*
var Assets embed.FS

//go:embed templates/*
var Templates embed.FS

type asset struct {
	name string
	url  string
	etag string
	data []byte
}

var staticAssets = loadAssets()

func loadAssets() map[string]asset {
	entries, err := fs.ReadDir(Assets, "assets")
	if err != nil {
		panic(err)
	}
	result := make(map[string]asset)
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) == ".html" {
			continue
		}
		data, err := Assets.ReadFile("assets/" + entry.Name())
		if err != nil {
			panic(err)
		}
		hash := fmt.Sprintf("%x", sha256.Sum256(data))
		ext := path.Ext(entry.Name())
		url := "/assets/" + strings.TrimSuffix(entry.Name(), ext) + "." + hash + ext
		item := asset{name: entry.Name(), url: url, etag: `"` + hash + `"`, data: data}
		result["/"+entry.Name()] = item
		result[url] = item
	}
	return result
}

// AssetURL returns the content-versioned URL of an embedded browser resource.
func AssetURL(name string) string {
	item, ok := staticAssets["/"+name]
	if !ok {
		panic("unknown browser asset: " + name)
	}
	return item.url
}

func ServeAsset(w http.ResponseWriter, r *http.Request) {
	item, ok := staticAssets[r.URL.Path]
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	if r.URL.Path == item.url {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	w.Header().Set("ETag", item.etag)
	http.ServeContent(w, r, item.name, time.Time{}, bytes.NewReader(item.data))
}
