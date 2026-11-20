//go:build embedui

package main

import (
	"embed"
	"io/fs"
)

//go:embed all:web/build
var bundledDashboard embed.FS

func dashboardAssets() (fs.FS, error) { return fs.Sub(bundledDashboard, "web/build") }
