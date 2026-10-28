package main

import (
	"embed"
	"fmt"
	"log"
	"runtime"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	application.RegisterEvent[AppState]("state")
}

var browserLabels = map[string]string{"chrome": "Chrome", "brave": "Brave", "firefox": "Firefox", "safari": "Safari"}

func main() {
	svc := NewPorthole()
	app := application.New(application.Options{
		Name:        "Porthole",
		Description: "Open a browser through a chosen region without changing the system network",
		Services:    []application.Service{application.NewService(svc)},
		Assets:      application.AssetOptions{Handler: application.AssetFileServerFS(assets)},
		Mac: application.MacOptions{
			// Closing the window keeps tunnels and the menu-bar icon alive.
			ApplicationShouldTerminateAfterLastWindowClosed: false,
		},
		SingleInstance: &application.SingleInstanceOptions{UniqueID: "io.github.ashutosh091.porthole"},
	})

	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "Porthole",
		Width:     920,
		Height:    640,
		MinWidth:  640,
		MinHeight: 420,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 44,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(22, 23, 27),
		URL:              "/",
	})
	window.RegisterHook(events.Common.WindowClosing, func(e *application.WindowEvent) {
		window.Hide()
		e.Cancel()
	})
	showWindow := func() {
		window.Show()
		window.Focus()
	}
	app.Event.OnApplicationEvent(events.Mac.ApplicationShouldHandleReopen, func(*application.ApplicationEvent) { showWindow() })

	tray := app.SystemTray.New()
	if runtime.GOOS == "darwin" {
		tray.SetTemplateIcon(trayIcon())
	} else {
		tray.SetIcon(trayIcon())
	}
	tray.SetTooltip("Porthole")
	tray.SetMenu(buildTrayMenu(app, svc, svc.State(), showWindow))
	svc.onChange = func(st AppState) {
		tray.SetMenu(buildTrayMenu(app, svc, st, showWindow))
		up := 0
		for _, p := range st.Profiles {
			if p.Status.State == "up" {
				up++
			}
		}
		if up > 0 {
			tray.SetLabel(fmt.Sprint(up))
		} else {
			tray.SetLabel("")
		}
	}

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// buildTrayMenu lists every runnable profile with its browsers, plus global actions.
func buildTrayMenu(app *application.App, svc *Porthole, st AppState, showWindow func()) *application.Menu {
	menu := app.NewMenu()
	notify := func(err error) {
		if err != nil {
			app.Dialog.Error().SetTitle("Porthole").SetMessage(err.Error()).Show()
		}
	}
	if st.Safari != nil {
		menu.Add(fmt.Sprintf("Stop Safari mode (%s)", st.Safari.Profile)).OnClick(func(*application.Context) {
			go func() { notify(svc.StopSafari()) }()
		})
		menu.AddSeparator()
	}
	runnable := 0
	for _, p := range st.Profiles {
		if p.Kind == "cli-only" {
			continue
		}
		runnable++
		name := p.Name
		sub := menu.AddSubmenu(trayLabel(p))
		for _, b := range st.Browsers {
			browser := b
			label := "Open " + browserLabels[b]
			if b == "safari" {
				label = "Open Safari (streaming sites)"
			}
			sub.Add(label).OnClick(func(*application.Context) {
				go func() { notify(svc.Launch(name, browser)) }()
			})
		}
		sub.AddSeparator()
		if p.Status.State == "up" {
			sub.Add("Disconnect").OnClick(func(*application.Context) { go svc.Disconnect(name) })
		} else {
			sub.Add("Connect").OnClick(func(*application.Context) {
				go func() { _, err := svc.Connect(name); notify(err) }()
			})
		}
	}
	if runnable == 0 {
		menu.Add("No profiles yet").SetEnabled(false)
	}
	menu.AddSeparator()
	menu.Add("Import WireGuard config…").OnClick(func(*application.Context) {
		go func() { _, err := svc.ImportConfs(); notify(err) }()
	})
	menu.Add("Disconnect all").OnClick(func(*application.Context) { go svc.DisconnectAll() })
	menu.Add("Open Porthole…").OnClick(func(*application.Context) { showWindow() })
	menu.AddSeparator()
	menu.Add("Quit Porthole").OnClick(func(*application.Context) { app.Quit() })
	return menu
}

func trayLabel(p ProfileView) string {
	dot := "○"
	switch p.Status.State {
	case "up":
		dot = "●"
	case "connecting":
		dot = "◐"
	case "error":
		dot = "⚠︎"
	}
	where := strings.TrimSpace(p.ExpectCountry)
	if e := p.Status.Exit; e != nil && p.Status.State == "up" {
		where = strings.TrimSpace(e.City + ", " + e.Country)
	}
	if where != "" {
		return fmt.Sprintf("%s  %s — %s", dot, p.Name, where)
	}
	return fmt.Sprintf("%s  %s", dot, p.Name)
}
