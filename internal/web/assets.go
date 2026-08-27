package web

import "embed"

// staticFiles contains the dependency-free browser workbench.
//
//go:embed static/*
var staticFiles embed.FS
