//go:build !darwin || server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

func applyWindowAppearance(application.Window, string) {}
