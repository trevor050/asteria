package main

import (
	"embed"
	"io/fs"
	"log"
	"os"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/events"
)

//go:embed all:frontend/dist
var frontendFS embed.FS

//go:embed skills/core
var skillsFS embed.FS

func main() {
	core, err := fs.Sub(skillsFS, ".")
	if err != nil {
		log.Fatal(err)
	}
	svc, err := NewApp(core)
	if err != nil {
		log.Fatal(err)
	}
	preview := &previewHandler{store: svc.store, tools: svc.tools}

	var window *application.WebviewWindow
	app := application.New(application.Options{
		Name:        "Asteria",
		Description: "Convert files locally from a command bar",
		Services:    []application.Service{application.NewService(svc)},
		Assets: application.AssetOptions{
			Handler:    application.AssetFileServerFS(frontendFS),
			Middleware: preview.wrap,
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "io.github.trevor050.asteria",
			OnSecondInstanceLaunch: func(data application.SecondInstanceData) {
				if window != nil {
					window.Restore()
					window.Focus()
				}
				// Args is the second process's full argv; skip the program path.
				if len(data.Args) > 1 {
					svc.addFromArgs(data.Args[1:])
				}
			},
		},
		Server:     application.ServerOptions{Host: "127.0.0.1", Port: 34217},
		OnShutdown: svc.shutdown,
	})

	window = app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:      "main",
		Title:     "Asteria",
		Width:     980,
		Height:    680,
		MinWidth:  560,
		MinHeight: 440,
		Mac: application.MacWindow{
			// Real Liquid Glass behind the interface on macOS 26; Wails
			// falls back to the translucent material on older versions.
			Backdrop: application.MacBackdropLiquidGlass,
			LiquidGlass: application.MacLiquidGlass{
				Style:    application.LiquidGlassStyleAutomatic,
				Material: application.NSVisualEffectMaterialAuto,
			},
			TitleBar:                application.MacTitleBarHiddenInset,
			InvisibleTitleBarHeight: 44,
		},
		BackgroundColour: application.NewRGBA(0, 0, 0, 0),
		URL:              "/",
		EnableFileDrop:   true,
	})

	window.OnWindowEvent(events.Common.WindowFilesDropped, func(event *application.WindowEvent) {
		files := event.Context().DroppedFiles()
		if len(files) == 0 {
			return
		}
		app.Event.Emit(eventFilesAdded, svc.AddPaths(files))
	})

	svc.attach(app, window)

	// Files passed on the command line ("Open with", drag onto the app icon
	// on Windows/Linux) are added on start.
	go svc.addFromArgs(os.Args[1:])

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}
