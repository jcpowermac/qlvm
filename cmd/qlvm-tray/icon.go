// Package main embeds the tray icon: two stacked VM slabs with LED
// cut-outs (a "VM stack" glyph), neutral grey on a transparent background
// so it reads on both light and dark panels. State is conveyed in the menu,
// not the icon (v1 keeps the icon static).
package main

import "encoding/base64"

// iconPNG is a 48x48 StatusNotifierItem icon PNG.
var iconPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAADAAAAAwCAYAAABXAvmHAAABAUlEQVR4nGJiGOJg1AMDDUY9MNBg1AMDDUY9MNCABZ/kP36VqfNm9Mf++P6dl35OQgAOTs7PSRmFi5k+3snGpYYRlwTI8dP627Jo5joSQFZh1TRcnsDpgTnLN38aqJBHB6CYSIn05cMmhzMJ0dvxLtmzTDW+7zkDYt/gdDHZMzXtNDFuGTSZGOZ4dDYhMGg8QC4YNB4AJRtsbEJgyGdinDEAKn9p6ioSAD634IwBhiFSkQ15MGgyMblg1AMDDUY9MNBg1AMDDUY9MNBgtE9MDzDaJ6YHGO0TD1UwaDww2idGB6N9YiqA0T7xUACjHhhoMOqBgQajHhhoMOQ9AAgAAP//jLyToU9DdRkAAAAASUVORK5CYII=")
