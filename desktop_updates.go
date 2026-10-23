package main

import "github.com/egoist/mygo/plugins/updater"

// Use a command so the native menu and command palette share the same action.
// Creating the menu does not require initializing the plugin in headless tests.
var cmdCheckUpdates = command{
	ID:    "app.check-updates",
	Title: "检查更新…",
	Run:   func(*App) { updater.CheckForUpdates() },
}
