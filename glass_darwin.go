//go:build darwin && !server

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// asteriaSetAppearance makes the window follow Asteria's theme setting
// (0 system, 1 light, 2 dark), so the native Liquid Glass behind the web
// content matches the interface drawn on it.
static void asteriaSetAppearance(void *win, int mode) {
	if (win == NULL) {
		return;
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		NSWindow *w = (__bridge NSWindow *)win;
		if (mode == 1) {
			w.appearance = [NSAppearance appearanceNamed:NSAppearanceNameAqua];
		} else if (mode == 2) {
			w.appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
		} else {
			w.appearance = nil;
		}
	});
}
*/
import "C"

import (
	"strconv"
	"strings"

	"github.com/wailsapp/wails/v3/pkg/application"
	"golang.org/x/sys/unix"
)

// nativeGlass reports whether the window has Liquid Glass behind it:
// macOS 26 or later (older versions get Wails' translucent fallback, which
// the interface covers with its own background).
func nativeGlass() bool {
	v, err := unix.Sysctl("kern.osproductversion")
	if err != nil {
		return false
	}
	major, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return err == nil && major >= 26
}

func applyWindowAppearance(w application.Window, appearance string) {
	if w == nil {
		return
	}
	mode := 0
	switch appearance {
	case "light":
		mode = 1
	case "dark":
		mode = 2
	}
	C.asteriaSetAppearance(w.NativeWindow(), C.int(mode))
}
