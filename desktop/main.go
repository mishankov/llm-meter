package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"

	"github.com/mishankov/llm-meter/internal/relay"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed icons/tray-template.png
var trayIcon []byte

//go:embed icons/tray-alert-template.png
var trayAlertIcon []byte

//go:embed frontend/src/assets/llm-meter.png
var appIcon []byte

const eventSnapshotUpdated = "relay:snapshot"

func init() {
	application.RegisterEvent[AppSnapshot](eventSnapshotUpdated)
}

func main() {
	store, err := DefaultConfigStore()
	if err != nil {
		log.Fatal(err)
	}
	manager, err := NewRelayManager(store, nil)
	if err != nil {
		log.Fatal(err)
	}
	service := NewRelayService(manager)

	var app *application.App
	var window *application.WebviewWindow
	var createWindow sync.Once
	openWindow := func(settings bool) {
		setDockIconVisible(true, appIcon)
		createWindow.Do(func() {
			url := "/"
			if settings {
				url = "/#settings"
			}
			window = app.Window.NewWithOptions(application.WebviewWindowOptions{
				Title: "LLM Meter Relay",
				Width: 720, Height: 580, MinWidth: 560, MinHeight: 460,
				URL: url,
				Mac: application.MacWindow{
					TitleBar:                application.MacTitleBarHiddenInset,
					InvisibleTitleBarHeight: 52,
				},
			})
			window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
				window.Hide()
				setDockIconVisible(false, nil)
				e.Cancel()
			})
		})
		window.UnMinimise()
		window.Show().Focus()
		if settings {
			window.ExecJS("window.dispatchEvent(new Event('relay-open-settings'))")
		}
	}

	app = application.New(application.Options{
		Name:        "LLM Meter Relay",
		Description: "Local macOS menu-bar relay for LLM Meter",
		Icon:        appIcon,
		Services: []application.Service{
			application.NewService(service),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ActivationPolicy: application.ActivationPolicyAccessory,
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "com.mishankov.llm-meter-relay",
			OnSecondInstanceLaunch: func(_ application.SecondInstanceData) {
				openWindow(true)
			},
		},
		OnShutdown: func() {
			manager.SetOnChange(nil)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			_ = manager.Stop(ctx)
		},
	})

	service.bindApp(app)
	tray := app.SystemTray.New()
	tray.SetTemplateIcon(trayIcon)
	refresh := setupTray(app, tray, service, openWindow)

	initial := manager.Snapshot()
	refresh(initial)
	// A slow menu or webview must never block request metering or shutdown.
	changed := make(chan struct{}, 1)
	manager.SetOnChange(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(_ *application.ApplicationEvent) {
		openWindow(false)
	})
	app.Event.OnApplicationEvent(events.Common.ApplicationStarted, func(_ *application.ApplicationEvent) {
		go func() {
			for {
				select {
				case <-app.Context().Done():
					return
				case <-changed:
					snap := manager.Snapshot()
					application.InvokeSync(func() { refresh(snap) })
					app.Event.Emit(eventSnapshotUpdated, snap)
				}
			}
		}()
		if !initial.Configured {
			openWindow(false)
		} else if initial.Config.AutoStart {
			if err := manager.Start(); err != nil {
				openWindow(false)
			}
		}
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

func setupTray(app *application.App, tray *application.SystemTray, service *RelayService, openWindow func(bool)) func(AppSnapshot) {
	menu := app.NewMenu()
	status := menu.Add("Relay").SetEnabled(false)
	detail := menu.Add("").SetEnabled(false)
	menu.AddSeparator()
	start := menu.Add("Start Relay").SetAccelerator("Cmd+Return").OnClick(func(_ *application.Context) {
		if _, err := service.StartRelay(); err != nil {
			openWindow(false)
		}
	})
	stop := menu.Add("Stop Relay").SetAccelerator("Cmd+Return").OnClick(func(_ *application.Context) {
		if _, err := service.StopRelay(); err != nil {
			openWindow(false)
		}
	})
	retry := menu.Add("Retry Pending Uploads").OnClick(func(_ *application.Context) { service.RetryUploads() })

	copyMenu := menu.AddSubmenu("Copy Client URL")
	endpoints := make(map[string]*application.MenuItem)
	for _, endpoint := range buildEndpoints(relay.DefaultListen, "") {
		provider := endpoint.Provider
		endpoints[provider] = copyMenu.Add(endpoint.Label).OnClick(func(_ *application.Context) {
			for _, ep := range service.GetSnapshot().Endpoints {
				if ep.Provider == provider {
					service.CopyText(ep.URL)
					return
				}
			}
		})
	}
	dashboard := menu.Add("Open Dashboard").OnClick(func(_ *application.Context) { _ = service.OpenDashboard() })
	menu.AddSeparator()
	menu.Add("Settings & Setup…").SetAccelerator("Cmd+,").OnClick(func(_ *application.Context) { openWindow(true) })
	menu.Add("Quit LLM Meter Relay").SetAccelerator("Cmd+Q").OnClick(func(_ *application.Context) { app.Quit() })
	tray.SetMenu(menu)

	alertIcon := false
	return func(snap AppSnapshot) {
		line := formatTrayStatus(snap)
		status.SetLabel(line)
		tray.SetTooltip(line)
		detailText := formatTrayDetail(snap)
		detail.SetLabel(detailText).SetHidden(detailText == "")
		alert := snap.Status.LastError != "" || snap.Status.State == relay.StateError ||
			(snap.Status.Outbox != nil && snap.Status.Outbox.LastError != "")
		if alert != alertIcon {
			alertIcon = alert
			if alert {
				tray.SetTemplateIcon(trayAlertIcon)
			} else {
				tray.SetTemplateIcon(trayIcon)
			}
		}
		draining := snap.Status.State == relay.StateDraining
		running := snap.Status.State == relay.StateRunning
		start.SetEnabled(snap.Configured && !running && !draining).SetHidden(running || draining)
		stop.SetEnabled(running).SetHidden(!running && !draining)
		retry.SetHidden(snap.Status.Outbox == nil || snap.Status.Outbox.Pending == 0)
		for provider, item := range endpoints {
			item.SetHidden(!slices.Contains(snap.Status.Routes, provider))
		}
		dashboard.SetEnabled(snap.Configured)
	}
}

func formatTrayStatus(snap AppSnapshot) string {
	switch snap.Status.State {
	case relay.StateRunning:
		if snap.Status.Outbox != nil && snap.Status.Outbox.LastError != "" {
			return fmt.Sprintf("▲ Attention · %s", snap.Status.Mode)
		}
		return fmt.Sprintf("● Running · %s", snap.Status.Mode)
	case relay.StateDraining:
		return "◌ Draining outbox…"
	case relay.StateError:
		return "▲ Relay error"
	default:
		if !snap.Configured {
			return "○ Setup required"
		}
		return "○ Stopped"
	}
}

func formatTrayDetail(snap AppSnapshot) string {
	if snap.Status.LastError != "" {
		return snap.Status.LastError
	}
	if snap.Status.Outbox != nil {
		if snap.Status.Outbox.LastError != "" {
			return fmt.Sprintf("Outbox: %d pending · %s", snap.Status.Outbox.Pending, snap.Status.Outbox.LastError)
		}
		if !snap.Status.Outbox.LastSuccessAt.IsZero() {
			return fmt.Sprintf("Outbox: %d pending · Last upload %s", snap.Status.Outbox.Pending, snap.Status.Outbox.LastSuccessAt.Local().Format("15:04"))
		}
		return fmt.Sprintf("Outbox: %d pending", snap.Status.Outbox.Pending)
	}
	if snap.Status.State == relay.StateRunning {
		return fmt.Sprintf("Listening on http://%s", snap.Status.ListenAddr)
	}
	if snap.PendingOnDisk > 0 {
		return fmt.Sprintf("Outbox: %d pending on disk", snap.PendingOnDisk)
	}
	return ""
}
