// Optional Wails runtime shim.
// @wailsio/runtime will try to load this at /wails/custom.js.
//
// Some older platform integrations (or mixed runtime versions) may call
// `window.wails.Window.HandlePlatformFileDrop`. Wails v3 runtime exposes this
// handler on `window._wails.handlePlatformFileDrop`.
(function () {
  try {
    window.wails = window.wails || {};
    window.wails.Window = window.wails.Window || {};

    if (typeof window.wails.Window.HandlePlatformFileDrop !== 'function') {
      window.wails.Window.HandlePlatformFileDrop = function (filenames, x, y) {
        if (window._wails && typeof window._wails.handlePlatformFileDrop === 'function') {
          return window._wails.handlePlatformFileDrop(filenames, x, y);
        }
      };
    }
  } catch (_) {
    // Intentionally ignore.
  }
})();
