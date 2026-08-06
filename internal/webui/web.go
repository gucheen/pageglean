package webui

import "embed"

// Assets contains the browser application.
//
//go:embed assets/*
var Assets embed.FS
