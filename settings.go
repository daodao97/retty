package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"retty/internal/atomicfile"
	"retty/internal/rex"
)

// settings are the user's choices, kept in settings.json beside the
// server's state.
type settings struct {
	// Appearance is "light", "dark", or "" to follow the system.
	Appearance                       string  `json:"appearance,omitempty"`
	DesktopLayout                    string  `json:"desktopLayout,omitempty"`
	SidebarSessionMode               string  `json:"sidebarSessionMode,omitempty"`
	TitleFolderPosition              string  `json:"titleFolderPosition,omitempty"`
	SidebarCollapsed                 bool    `json:"sidebarCollapsed,omitempty"`
	FilesOpen                        bool    `json:"filesOpen,omitempty"`
	FilesHidden                      bool    `json:"filesHidden,omitempty"`
	FilesDiff                        bool    `json:"filesDiff,omitempty"`
	FilesDiffBase                    string  `json:"filesDiffBase,omitempty"`
	FilesSidePanel                   bool    `json:"filesSidePanel,omitempty"`
	FilesWidth                       float32 `json:"filesWidth,omitempty"`
	HideGitStatus                    bool    `json:"hideGitStatus,omitempty"`
	HideUsageTray                    bool    `json:"hideUsageTray,omitempty"`
	UsageTrayAgent                   string  `json:"usageTrayAgent,omitempty"`
	UsageTrayAgentIcon               bool    `json:"usageTrayAgentIcon,omitempty"`
	FontSize                         float32 `json:"fontSize,omitempty"`
	LinkEditor                       string  `json:"linkEditor,omitempty"`
	CopyRawText                      bool    `json:"copyRawText,omitempty"`
	HideDesktopNotifications         bool    `json:"hideDesktopNotifications,omitempty"`
	HidePhoneNotifications           bool    `json:"hidePhoneNotifications,omitempty"`
	HideAgentNotifications           bool    `json:"hideAgentNotifications,omitempty"`
	HideAgentCompletionNotifications bool    `json:"hideAgentCompletionNotifications,omitempty"`
}

const defaultFontSize = 11.6

var prefs settings

func settingsPath() string { return filepath.Join(rex.Dir(), "settings.json") }

func loadSettings() {
	prefs = readSettings()
	termFont.Size = prefs.FontSize
	applyAppearance()
}

func readSettings() settings {
	var s settings
	if b, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.FontSize < 6 || s.FontSize > 40 {
		s.FontSize = defaultFontSize
	}
	switch s.LinkEditor {
	case "", "vscode", "cursor", "system":
	default:
		s.LinkEditor = ""
	}
	if s.TitleFolderPosition != "before" && s.TitleFolderPosition != "none" {
		s.TitleFolderPosition = ""
	}
	if s.DesktopLayout != "sidebar" {
		s.DesktopLayout = ""
	}
	if s.SidebarSessionMode != "project" {
		s.SidebarSessionMode = ""
	}
	if s.UsageTrayAgent != "codex" && s.UsageTrayAgent != "claude" {
		s.UsageTrayAgent = ""
	}
	if s.FilesDiffBase != "turn" && s.FilesDiffBase != "branch" {
		s.FilesDiffBase = "head"
	}
	return s
}

func (a *App) setDesktopLayout(layout string) {
	if layout != "sidebar" {
		layout = ""
	}
	prefs.DesktopLayout = layout
	a.sidebarVisibleTab = nil
	saveSettings()
	if !a.settingsOpen {
		a.focusCurrentTerminal()
	}
}

func (a *App) focusCurrentTerminal() {
	if t := a.tab(); t != nil && t.Focus != nil {
		if t.Focus.terminal() != nil && t.Focus.terminal().find.open {
			t.Focus.terminal().find.focus = true
		} else {
			a.focusReq = t.Focus
		}
	}
}

// showTabSidebar switches to the sidebar layout with the sidebar open.
func (a *App) showTabSidebar() {
	prefs.SidebarCollapsed = false
	a.setDesktopLayout("sidebar")
}

func (a *App) toggleTabSidebar() {
	if prefs.DesktopLayout != "sidebar" {
		return
	}
	prefs.SidebarCollapsed = !prefs.SidebarCollapsed
	a.sidebarVisibleTab = nil
	saveSettings()
	a.focusCurrentTerminal()
}

func (a *App) setLinkEditor(editor string) {
	prefs.LinkEditor = editor
	saveSettings()
}

func (a *App) setTitleFolderPosition(position string) {
	prefs.TitleFolderPosition = position
	saveSettings()
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) setSidebarSessionMode(mode string) {
	prefs.SidebarSessionMode = mode
	a.sidebarVisibleTab = nil
	saveSettings()
}

func (a *App) setCopyRawText(raw bool) {
	prefs.CopyRawText = raw
	for _, p := range a.allPanes() {
		if p.terminalView() != nil {
			p.terminal().term.SetCopyRawText(raw)
		}
	}
	saveSettings()
}

func saveSettings() {
	b, _ := json.MarshalIndent(prefs, "", "  ")
	atomicfile.Write(settingsPath(), b)
}

func (a *App) setGitStatus(show bool) {
	prefs.HideGitStatus = !show
	a.gitStatus = nil
	saveSettings()
}

func applyAppearance() {
	switch prefs.Appearance {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func (a *App) setAppearance(v string) {
	prefs.Appearance = v
	applyAppearance()
	saveSettings()
	if it := a.appearanceItems[v]; it != nil {
		it.SetChecked(true)
	}
}

// setFontSize changes the size of the terminals' text, in every pane.
func (a *App) setFontSize(size float32) {
	size = min(max(size, 8), 32)
	prefs.FontSize = size
	termFont.Size = size
	for _, p := range a.allPanes() {
		if p.terminalView() != nil {
			p.terminal().term.SetFont(termFont)
		}
	}
	saveSettings()
}
