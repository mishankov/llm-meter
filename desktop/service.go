package main

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// AutostartStatus reports whether the app is configured to launch at macOS login.
type AutostartStatus struct {
	Enabled   bool   `json:"enabled"`
	Available bool   `json:"available"`
	Strategy  string `json:"strategy,omitempty"`
	Error     string `json:"error,omitempty"`
}

// RelayService is the Wails v3 bound service called by the Svelte settings window.
type RelayService struct {
	app     *application.App
	manager *RelayManager
}

func NewRelayService(manager *RelayManager) *RelayService {
	return &RelayService{manager: manager}
}

func (s *RelayService) bindApp(app *application.App) {
	s.app = app
}

func (s *RelayService) GetSnapshot() AppSnapshot {
	return s.manager.Snapshot()
}

func (s *RelayService) SaveConfig(req SaveConfigRequest) (AppSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.manager.SaveConfig(ctx, req); err != nil {
		return s.manager.Snapshot(), err
	}
	return s.manager.Snapshot(), nil
}

func (s *RelayService) StartRelay() (AppSnapshot, error) {
	if err := s.manager.Start(); err != nil {
		return s.manager.Snapshot(), err
	}
	return s.manager.Snapshot(), nil
}

func (s *RelayService) StopRelay() (AppSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if err := s.manager.Stop(ctx); err != nil {
		return s.manager.Snapshot(), err
	}
	return s.manager.Snapshot(), nil
}

func (s *RelayService) DrainAndStopRelay() (AppSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.manager.DrainAndStop(ctx); err != nil {
		return s.manager.Snapshot(), err
	}
	return s.manager.Snapshot(), nil
}

func (s *RelayService) RetryUploads() AppSnapshot {
	s.manager.RetryUploads()
	return s.manager.Snapshot()
}

func (s *RelayService) UploadKeptReports() (AppSnapshot, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := s.manager.UploadKeptReports(ctx)
	return s.manager.Snapshot(), err
}

func (s *RelayService) ClearLogs() AppSnapshot {
	s.manager.ClearLogs()
	return s.manager.Snapshot()
}

func (s *RelayService) TestConnection(target string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	return s.manager.TestConnection(ctx, target)
}

func (s *RelayService) CopyText(text string) bool {
	if s.app == nil {
		return false
	}
	return s.app.Clipboard.SetText(text)
}

func (s *RelayService) OpenDashboard() error {
	if s.app == nil {
		return errors.New("application not initialized")
	}
	target := strings.TrimSpace(s.manager.Snapshot().Config.Target)
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return errors.New("configure a valid HTTPS server URL first")
	}
	return s.app.Browser.OpenURL(u.String())
}

func (s *RelayService) GetAutostart() AutostartStatus {
	if s.app == nil {
		return AutostartStatus{Available: false}
	}
	st, err := s.app.Autostart.Status()
	if err != nil {
		return AutostartStatus{Available: false, Error: err.Error()}
	}
	return AutostartStatus{
		Enabled:   st.Enabled,
		Available: true,
		Strategy:  string(st.Strategy),
	}
}

func (s *RelayService) SetAutostart(enabled bool) (AutostartStatus, error) {
	if s.app == nil {
		return AutostartStatus{}, errors.New("application not initialized")
	}
	var err error
	if enabled {
		err = s.app.Autostart.Enable()
	} else {
		err = s.app.Autostart.Disable()
	}
	st := s.GetAutostart()
	return st, err
}

func (s *RelayService) GenerateAntigravityLauncher(binaryPath string) (string, error) {
	snap := s.manager.Snapshot()
	listen := snap.Status.ListenAddr
	if listen == "" {
		listen = snap.Config.Listen
	}
	return BuildAntigravityLauncher(listen, binaryPath)
}

func (s *RelayService) InstallAntigravityLauncher(destPath, binaryPath string) (string, error) {
	snap := s.manager.Snapshot()
	listen := snap.Status.ListenAddr
	if listen == "" {
		listen = snap.Config.Listen
	}
	return InstallAntigravityLauncher(destPath, listen, binaryPath)
}

func (s *RelayService) PickAntigravityBinary() (string, error) {
	if s.app == nil {
		return "", errors.New("application not initialized")
	}
	path, err := s.app.Dialog.OpenFile().
		SetTitle("Select Antigravity Executable").
		CanChooseFiles(true).
		CanChooseDirectories(false).
		ShowHiddenFiles(true).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return path, nil
}

func (s *RelayService) Quit() {
	if s.app != nil {
		s.app.Quit()
	}
}
