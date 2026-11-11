//go:build !darwin || server

package main

import "github.com/wailsapp/wails/v3/pkg/application"

// nativeGlass is only available in the native macOS build.
func nativeGlass() bool { return false }

func applyWindowAppearance(application.Window, string) {}
