//go:build darwin && !server

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#import <Cocoa/Cocoa.h>

// asteriaSetAppearance makes the window follow Asteria's theme setting
// (0 system, 1 light, 2 dark), so the title bar controls match the interface.
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

import "github.com/wailsapp/wails/v3/pkg/application"

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
