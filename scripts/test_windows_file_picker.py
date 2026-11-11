#!/usr/bin/env python3
r"""Check Ctrl+O in an already-running native Asteria window.

Run on the interactive Windows desktop with pywinauto and Pillow installed:
  python scripts/test_windows_file_picker.py --pid PID --input C:\path\photo.png --report result.json

This test never launches Asteria or a browser. Launch the app separately using
its persistent test profile and the shared strike gate before running this test.
"""
import argparse
import json
from pathlib import Path
import sys
import time


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--pid", type=int, required=True)
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--report", type=Path, required=True)
    args = parser.parse_args()
    if sys.platform != "win32":
        parser.error("requires the interactive Windows desktop")
    from pywinauto import Desktop, keyboard

    report = {"passed": False, "input": str(args.input), "pid": args.pid}
    dialog = None
    try:
        assert args.input.is_file(), "input file does not exist"
        report["stage"] = "locate-main-window"
        native = Desktop(backend="win32").window(process=args.pid, class_name="WailsWebviewWindow")
        main_window = Desktop(backend="uia").window(handle=native.handle)
        main_window.set_focus()
        time.sleep(0.5)
        report["stage"] = "open-picker"
        keyboard.send_keys("^o", vk_packet=False)
        time.sleep(0.8)
        native_dialog = Desktop(backend="win32").window(process=args.pid, class_name="#32770")
        native_dialog.wait("visible", timeout=10)
        dialog = Desktop(backend="uia").window(handle=native_dialog.handle)
        report["stage"] = "inspect-picker"
        primary = dialog.child_window(auto_id="1", control_type="SplitButton")
        if not primary.exists(timeout=0.2):
            primary = dialog.child_window(auto_id="1", control_type="Button")
        report["primaryButton"] = primary.window_text()
        assert report["primaryButton"] == "Open", "Add files opened a folder-only dialog: " + report["primaryButton"]
        # Set the real common-dialog filename field, then use its Open button.
        report["stage"] = "select-file"
        filename = dialog.child_window(auto_id="1148", control_type="Edit")
        filename.click_input()
        keyboard.send_keys("^a", vk_packet=False)
        escapes = {"{": "{{}", "}": "{}}", "+": "{+}", "^": "{^}", "%": "{%}", "~": "{~}", "(": "{(}", ")": "{)}"}
        text = "".join(escapes.get(char, char) for char in str(args.input))
        keyboard.send_keys(text, with_spaces=True, pause=0.01)
        keyboard.send_keys("{ENTER}", vk_packet=False)
        native_dialog.wait_not("visible", timeout=10)
        report["stage"] = "verify-tray"
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            names = [item.window_text() for item in main_window.descendants(control_type="ListItem")]
            if any(args.input.name in name for name in names):
                report.update(passed=True, trayNames=names)
                break
            time.sleep(0.2)
        assert report["passed"], "selected file did not appear in the native tray"
    except Exception as exc:
        report["error"] = str(exc)
    finally:
        try:
            if dialog is not None and native_dialog.exists() and native_dialog.is_visible():
                dialog.child_window(auto_id="2", control_type="Button").click_input()
        except Exception as exc:
            report["cleanupError"] = str(exc)
        args.report.parent.mkdir(parents=True, exist_ok=True)
        args.report.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
