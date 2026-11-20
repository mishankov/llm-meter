//go:build !embedui

package main

import (
	"fmt"
	"io/fs"
	"os"
)

func dashboardAssets() (fs.FS, error) {
	ui := os.DirFS(valueOrDefault("DASHBOARD_DIR", "web/build"))
	if _, err := fs.Stat(ui, "index.html"); err != nil {
		return nil, fmt.Errorf("dashboard build unavailable; run npm ci --prefix web and npm run build --prefix web: %w", err)
	}
	return ui, nil
}
