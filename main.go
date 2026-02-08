package main

import (
	"embed"
	"log"
	"net/http"

	"github.com/wailsapp/wails/v3/pkg/application"
)

//go:embed all:frontend/dist all:skills/core
var assets embed.FS

func main() {
	// Create the App instance
	appInstance := NewApp()

	app := application.New(application.Options{
		Name:        "Asteria",
		Description: "File actions, chained live",
		Services: []application.Service{
			application.NewService(appInstance),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
			Middleware: func(next http.Handler) http.Handler {
				return next
			},
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// Create window with liquid glass
	window := app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "main",
		Title:  "Asteria",
		Width:  800,
		Height: 580,
		Mac: application.MacWindow{
			Backdrop: application.MacBackdropLiquidGlass,
			TitleBar: application.MacTitleBarHiddenInset,
			LiquidGlass: application.MacLiquidGlass{
				Style:        application.LiquidGlassStyleAutomatic,
				CornerRadius: 16.0,
				TintColor:    &application.RGBA{Red: 255, Green: 255, Blue: 255, Alpha: 20},
			},
			InvisibleTitleBarHeight: 50,
		},
		BackgroundColour: application.RGBA{Red: 255, Green: 255, Blue: 255, Alpha: 0},
		URL:              "/",
		EnableFileDrop:   true,
	})

	// Initialize app after we have the window
	appInstance.initWithApp(app, window)

	err := app.Run()
	if err != nil {
		log.Fatal(err)
	}
}
