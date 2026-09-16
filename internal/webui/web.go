package webui

import "embed"

// Assets contains the browser application.
//
//go:embed assets/*
var Assets embed.FS

//go:embed templates/*
var Templates embed.FS
