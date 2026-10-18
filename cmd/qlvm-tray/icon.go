// Package main embeds a small monochrome tray icon. The system tray
// (StatusNotifierItem) shows it next to the menu; it is deliberately plain —
// the per-VM state markers live in the menu, not the icon.
package main

import "encoding/base64"

// iconPNG is a 16x16 neutral-gray StatusNotifierItem icon.
var iconPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAAAAAA6mKC9AAAAFUlEQVR4nGLpYEAFTAwjWgAQAAD//6U4AKs3QbFZAAAAAElFTkSuQmCC")
