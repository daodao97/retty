package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// command is an action of the menus and the command palette.
type command struct {
	ID         string
	Automation bool // explicitly callable through the local CLI
	Target     bool // requires a stable session target
	Title      string
	// Accel is the menu item's shortcut, and Keys how the palette shows it.
	Accel, Keys string
	Run         func(a *App)
	// Hidden keeps it out of the palette.
	Hidden   bool
	Terminal bool // meaningful only for terminal content
}

func (cmd *command) available(a *App) bool {
	if !cmd.Terminal {
		return true
	}
	t := a.tab()
	return t != nil && t.Focus != nil && t.Focus.terminal() != nil
}

func (a *App) openFileComponent(diff bool) {
	var parent *Pane
	if t := a.tab(); t != nil {
		parent = t.Focus
	}
	a.openFilesPane(parent, a.currentDir(), diff, false)
}

var (
	cmdUsagePanel   = command{ID: "usage.open", Title: "用量概览…", Run: func(a *App) { a.toggleUsagePanel() }}
	cmdFilePane     = command{ID: "files.open", Automation: true, Target: true, Title: "新建文件窗格", Run: func(a *App) { a.openFileComponent(false) }}
	cmdDiffPane     = command{ID: "diff.open", Automation: true, Target: true, Title: "打开 Diff 窗格", Run: func(a *App) { a.openFileComponent(true) }}
	cmdTerminalPane = command{ID: "terminal.open", Title: "打开终端窗格", Run: func(a *App) {
		if a.tab() == nil {
			a.newTab(a.currentDir())
		} else {
			a.splitTerminal(false)
		}
	}}
	cmdBrowser = command{ID: "browser.open", Title: "打开浏览器窗格", Accel: "CmdOrCtrl+Shift+B", Keys: "⇧⌘B", Run: func(a *App) {
		var parent *Pane
		if t := a.tab(); t != nil {
			parent = t.Focus
		}
		if _, err := a.openBrowser(parent, "about:blank", a.currentHost(), false); err != nil {
			a.err = err.Error()
		}
	}}
	cmdFiles       = command{ID: "files.toggle", Automation: true, Target: true, Title: "文件浏览器", Accel: "CmdOrCtrl+Shift+E", Keys: "⇧⌘E", Run: func(a *App) { a.toggleFiles() }}
	cmdSettings    = command{ID: "settings.open", Automation: true, Target: false, Title: "Settings…", Accel: "CmdOrCtrl+,", Keys: "⌘,", Run: func(a *App) { a.openSettings() }}
	cmdNewTab      = command{ID: "tab.new", Automation: false, Target: false, Title: "New Tab", Accel: "CmdOrCtrl+T", Keys: "⌘T", Run: func(a *App) { a.newTab(a.currentDir()) }}
	cmdSplitRight  = command{ID: "pane.split-right", Automation: true, Target: true, Title: "Split Right", Accel: "CmdOrCtrl+D", Keys: "⌘D", Run: func(a *App) { a.split(false) }}
	cmdSplitDown   = command{ID: "pane.split-down", Automation: true, Target: true, Title: "Split Down", Accel: "CmdOrCtrl+Shift+D", Keys: "⇧⌘D", Run: func(a *App) { a.split(true) }}
	cmdClosePane   = command{ID: "pane.close", Automation: false, Target: true, Title: "Close Pane", Accel: "CmdOrCtrl+W", Keys: "⌘W", Run: func(a *App) { a.closeFocused() }}
	cmdCloseTab    = command{ID: "tab.close", Automation: false, Target: true, Title: "Close Tab", Accel: "CmdOrCtrl+Shift+W", Keys: "⇧⌘W", Run: func(a *App) { a.closeActiveTab() }}
	cmdZoom        = command{ID: "pane.zoom", Automation: true, Target: true, Title: "Zoom Pane", Accel: "CmdOrCtrl+Shift+Enter", Keys: "⇧⌘↩", Run: func(a *App) { a.toggleZoom() }}
	cmdEqualize    = command{ID: "pane.equalize", Automation: true, Target: true, Title: "Equalize Panes", Accel: "CmdOrCtrl+Ctrl+=", Keys: "⌃⌘=", Run: func(a *App) { a.equalize() }}
	cmdNextTab     = command{ID: "tab.next", Automation: true, Target: false, Title: "Next Tab", Accel: "Ctrl+Tab", Keys: "⌃Tab", Run: func(a *App) { a.cycleTab(1) }}
	cmdPrevTab     = command{ID: "tab.previous", Automation: true, Target: false, Title: "Previous Tab", Accel: "Ctrl+Shift+Tab", Keys: "⌃⇧Tab", Run: func(a *App) { a.cycleTab(-1) }}
	cmdRenameTab   = command{ID: "tab.rename", Automation: false, Target: true, Title: "Rename Tab…", Accel: "CmdOrCtrl+Shift+R", Keys: "⇧⌘R", Run: func(a *App) { a.startRename() }}
	cmdPalette     = command{ID: "palette.open", Automation: true, Target: false, Title: "Command Palette…", Accel: "CmdOrCtrl+Shift+P", Keys: "⇧⌘P", Run: func(a *App) { a.openPalette() }, Hidden: true}
	cmdPalette2    = command{ID: "pane.search", Automation: true, Target: false, Title: "Go to Pane…", Accel: "CmdOrCtrl+P", Keys: "⌘P", Run: func(a *App) { a.openPalette() }, Hidden: true}
	cmdFocusLeft   = command{ID: "pane.focus-left", Automation: true, Target: true, Title: "Focus Pane Left", Accel: "CmdOrCtrl+Alt+Left", Keys: "⌥⌘←", Run: func(a *App) { a.moveFocus(-1, 0) }}
	cmdFocusRight  = command{ID: "pane.focus-right", Automation: true, Target: true, Title: "Focus Pane Right", Accel: "CmdOrCtrl+Alt+Right", Keys: "⌥⌘→", Run: func(a *App) { a.moveFocus(1, 0) }}
	cmdFocusUp     = command{ID: "pane.focus-up", Automation: true, Target: true, Title: "Focus Pane Above", Accel: "CmdOrCtrl+Alt+Up", Keys: "⌥⌘↑", Run: func(a *App) { a.moveFocus(0, -1) }}
	cmdFocusDown   = command{ID: "pane.focus-down", Automation: true, Target: true, Title: "Focus Pane Below", Accel: "CmdOrCtrl+Alt+Down", Keys: "⌥⌘↓", Run: func(a *App) { a.moveFocus(0, 1) }}
	cmdGrowLeft    = command{ID: "pane.divider-left", Automation: true, Target: true, Title: "Move Divider Left", Accel: "CmdOrCtrl+Ctrl+Left", Keys: "⌃⌘←", Run: func(a *App) { a.resizeFocused(-1, 0) }}
	cmdGrowRight   = command{ID: "pane.divider-right", Automation: true, Target: true, Title: "Move Divider Right", Accel: "CmdOrCtrl+Ctrl+Right", Keys: "⌃⌘→", Run: func(a *App) { a.resizeFocused(1, 0) }}
	cmdGrowUp      = command{ID: "pane.divider-up", Automation: true, Target: true, Title: "Move Divider Up", Accel: "CmdOrCtrl+Ctrl+Up", Keys: "⌃⌘↑", Run: func(a *App) { a.resizeFocused(0, -1) }}
	cmdGrowDown    = command{ID: "pane.divider-down", Automation: true, Target: true, Title: "Move Divider Down", Accel: "CmdOrCtrl+Ctrl+Down", Keys: "⌃⌘↓", Run: func(a *App) { a.resizeFocused(0, 1) }}
	cmdClear       = command{ID: "terminal.clear", Terminal: true, Automation: false, Target: true, Title: "Clear Screen and Scrollback", Accel: "CmdOrCtrl+K", Keys: "⌘K", Run: func(a *App) { a.clearFocused() }, Hidden: true}
	cmdClearScroll = command{ID: "terminal.clear-scrollback", Terminal: true, Automation: false, Target: true, Title: "Clear Scrollback", Run: func(a *App) { a.clearFocused() }}
	cmdRestart     = command{ID: "session.restart", Terminal: true, Automation: false, Target: true, Title: "Restart Shell in Pane", Run: func(a *App) { a.restartFocused() }}
	cmdBigger      = command{ID: "font.increase", Automation: true, Target: false, Title: "Bigger Text", Accel: "CmdOrCtrl+=", Keys: "⌘+", Run: func(a *App) { a.setFontSize(termFont.Size + 1) }}
	cmdSmaller     = command{ID: "font.decrease", Automation: true, Target: false, Title: "Smaller Text", Accel: "CmdOrCtrl+-", Keys: "⌘−", Run: func(a *App) { a.setFontSize(termFont.Size - 1) }}
	cmdActualSize  = command{ID: "font.reset", Automation: true, Target: false, Title: "Actual Size", Accel: "CmdOrCtrl+0", Keys: "⌘0", Run: func(a *App) { a.setFontSize(defaultFontSize) }}
	cmdFind        = command{ID: "terminal.find", Automation: true, Target: true, Title: "Find…", Accel: "CmdOrCtrl+F", Keys: "⌘F", Run: func(a *App) { a.openFind() }}
	cmdHistory     = command{ID: "terminal.history", Terminal: true, Automation: true, Target: true, Title: "查看终端历史…", Run: func(a *App) { a.openTerminalHistory() }}
	cmdFindNext    = command{ID: "terminal.find-next", Automation: true, Target: true, Title: "Find Next", Accel: "CmdOrCtrl+G", Keys: "⌘G", Run: func(a *App) { a.findStep(1) }}
	cmdFindPrev    = command{ID: "terminal.find-previous", Automation: true, Target: true, Title: "Find Previous", Accel: "CmdOrCtrl+Shift+G", Keys: "⇧⌘G", Run: func(a *App) { a.findStep(-1) }}
	cmdLight       = command{ID: "appearance.light", Automation: true, Target: false, Title: "Appearance: Light", Run: func(a *App) { a.setAppearance("light") }}
	cmdDark        = command{ID: "appearance.dark", Automation: true, Target: false, Title: "Appearance: Dark", Run: func(a *App) { a.setAppearance("dark") }}
	cmdSystem      = command{ID: "appearance.system", Automation: true, Target: false, Title: "Appearance: System", Run: func(a *App) { a.setAppearance("") }}
	cmdEndAll      = command{ID: "session.end-all", Automation: false, Target: false, Title: "Quit and End All Sessions", Accel: "CmdOrCtrl+Alt+Q", Keys: "⌥⌘Q", Run: func(a *App) { a.quitAndEnd() }}
)

// paletteCommands are those the command palette lists, before the tabs.
var paletteCommands = []*command{
	&cmdInstallCLI,
	&cmdDiagnostics,
	&cmdBrowser,
	&cmdFiles,
	&cmdFilePane, &cmdDiffPane, &cmdTerminalPane,
	&cmdSettings,
	&cmdUsagePanel,
	&cmdGoalNew, &cmdGoalAttach, &cmdAutomations,
	&cmdCheckUpdates,
	&cmdUpdateRemote,
	&cmdNewTab, &cmdSplitRight, &cmdSplitDown, &cmdZoom, &cmdEqualize, &cmdClosePane, &cmdCloseTab,
	&cmdNextTab, &cmdPrevTab, &cmdRenameTab,
	&cmdFocusLeft, &cmdFocusRight, &cmdFocusUp, &cmdFocusDown,
	&cmdGrowLeft, &cmdGrowRight, &cmdGrowUp, &cmdGrowDown,
	&cmdBigger, &cmdSmaller, &cmdActualSize, &cmdLight, &cmdDark, &cmdSystem,
	&cmdClearScroll, &cmdRestart, &cmdEndAll,
	&cmdFind, &cmdFindNext, &cmdFindPrev,
	&cmdHistory,
}

// menu builds the menu bar, whose items run commands in the window.
func (a *App) menu() *mygo.Menu {
	a.commandItems = make(map[*command]*mygo.MenuItem)
	item := func(cmd *command) *mygo.MenuItem {
		it := &mygo.MenuItem{Label: cmd.Title, Accelerator: cmd.Accel, Disabled: !cmd.available(a), Click: func(*mygo.MenuItem, *mygo.Window) {
			a.run(cmd)
		}}
		if cmd.Terminal {
			a.commandItems[cmd] = it
		}
		return it
	}
	a.appearanceItems = make(map[string]*mygo.MenuItem)
	appearance := func(label, v string, cmd *command) *mygo.MenuItem {
		it := &mygo.MenuItem{Label: label, Type: mygo.MenuItemRadio, Checked: prefs.Appearance == v, Click: func(*mygo.MenuItem, *mygo.Window) {
			a.run(cmd)
		}}
		a.appearanceItems[v] = it
		return it
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			item(&cmdCheckUpdates),
			mygo.Separator(),
			item(&cmdSettings),
			item(&cmdInstallCLI),
			item(&cmdDiagnostics),
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide}, {Role: mygo.RoleHideOthers}, {Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Label: "Shell", Submenu: []*mygo.MenuItem{
			item(&cmdNewTab),
			mygo.Separator(),
			item(&cmdGoalNew),
			item(&cmdAutomations),
			mygo.Separator(),
			item(&cmdSplitRight),
			item(&cmdSplitDown),
			mygo.Separator(),
			// Control-Tab stays a menu accelerator so AppKit handles it
			// before its own key-view navigation.
			item(&cmdNextTab),
			item(&cmdPrevTab),
			item(&cmdRenameTab),
			mygo.Separator(),
			item(&cmdClosePane),
			item(&cmdCloseTab),
			mygo.Separator(),
			item(&cmdRestart),
			item(&cmdUpdateRemote),
			item(&cmdEndAll),
		}},
		{Label: "Edit", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleUndo},
			{Role: mygo.RoleRedo},
			mygo.Separator(),
			{Role: mygo.RoleCut},
			{Role: mygo.RoleCopy},
			{Role: mygo.RolePaste},
			{Role: mygo.RoleSelectAll},
			mygo.Separator(),
			item(&cmdFind), item(&cmdFindNext), item(&cmdFindPrev),
			mygo.Separator(),
			item(&cmdClear),
		}},
		{Label: "View", Submenu: []*mygo.MenuItem{
			item(&cmdTerminalPane),
			item(&cmdBrowser),
			item(&cmdFiles),
			item(&cmdFilePane),
			item(&cmdDiffPane),
			item(&cmdHistory),
			item(&cmdPalette),
			item(&cmdPalette2),
			mygo.Separator(),
			item(&cmdZoom),
			item(&cmdEqualize),
			mygo.Separator(),
			{Label: "Focus", Submenu: []*mygo.MenuItem{item(&cmdFocusLeft), item(&cmdFocusRight), item(&cmdFocusUp), item(&cmdFocusDown)}},
			{Label: "Resize", Submenu: []*mygo.MenuItem{item(&cmdGrowLeft), item(&cmdGrowRight), item(&cmdGrowUp), item(&cmdGrowDown)}},
			mygo.Separator(),
			item(&cmdBigger),
			item(&cmdSmaller),
			item(&cmdActualSize),
			{Label: "Appearance", Submenu: []*mygo.MenuItem{
				appearance("System", "", &cmdSystem),
				appearance("Light", "light", &cmdLight),
				appearance("Dark", "dark", &cmdDark),
			}},
			mygo.Separator(),
			{Role: mygo.RoleToggleFullScreen},
			{Role: mygo.RoleToggleDevTools, Label: "Inspector", Hidden: !mygo.IsDev()},
		}},
		{Role: mygo.RoleWindowMenu},
	})
}

func (a *App) syncCommandItems() {
	for cmd, item := range a.commandItems {
		if enabled := cmd.available(a); item.IsEnabled() != enabled {
			item.SetEnabled(enabled)
		}
	}
}

// do changes the state on the main thread, from a menu, and draws.
func (a *App) do(fn func()) {
	if a.win == nil {
		return
	}
	a.win.Update(fn)
}

// run runs a command of the menus, on the main thread. New Tab opens the
// window again when it was closed.
func (a *App) run(cmd *command) {
	// The updater has its own window, even after the terminal window closes.
	if cmd == &cmdCheckUpdates {
		cmd.Run(a)
		return
	}
	if a.win == nil {
		if cmd == &cmdNewTab || cmd == &cmdSettings {
			mygo.RunOnMain(func() {
				a.open()
				if cmd == &cmdSettings && a.win != nil {
					a.do(a.openSettings)
				}
			})
		}
		return
	}
	a.do(func() {
		if a.win != nil && cmd.available(a) {
			cmd.Run(a)
		}
	})
}

func (a *App) closeFocused() {
	if a.remoteUpdate != nil && a.remoteUpdate.open {
		a.remoteUpdate.open = false
		return
	}
	if a.terminalHistory != nil && a.terminalHistory.open {
		a.terminalHistory.close()
		return
	}
	if t := a.tab(); t != nil && t.Focus != nil {
		a.closePane(t.Focus)
	}
}

func (a *App) closeActiveTab() {
	if a.remoteUpdate != nil && a.remoteUpdate.open {
		a.remoteUpdate.open = false
		return
	}
	if a.terminalHistory != nil && a.terminalHistory.open {
		a.terminalHistory.close()
		return
	}
	if t := a.tab(); t != nil {
		a.closeTab(t)
	}
}

func (a *App) cycleTab(d int) {
	if n := len(a.tabs); n > 1 {
		a.selectTab(((a.active+d)%n + n) % n)
	}
}

func (a *App) startRename() {
	if t := a.tab(); t != nil {
		a.renaming, a.renameText, a.renameFocus = t, t.Name, false
		a.sidebarVisibleTab = nil
		a.focusReq = nil // the field takes the focus
	}
}

func (a *App) clearFocused() {
	if a.terminalHistory != nil && a.terminalHistory.open {
		return
	}
	if t := a.tab(); t != nil && t.Focus != nil && t.Focus.terminalView() != nil {
		// Clear the scrollback and the screen, and have the shell draw
		// its prompt again, as Command-K does in Terminal.
		p := t.Focus
		p.terminal().term.Feed([]byte("\x1b[H\x1b[2J\x1b[3J"))
		idle := p.terminal().info.Idle
		client := a.paneClient(p)
		if client == nil || p.terminal().SID == "" {
			return
		}
		go func() {
			if err := client.Clear(p.terminal().SID); err == nil && idle {
				p.terminal().term.Send([]byte{0x0c})
			}
		}()
	}
}

// restartFocused ends the focused pane's session and starts a shell in
// its place, in the same directory.
func (a *App) restartFocused() {
	if a.terminalHistory != nil && a.terminalHistory.open {
		return
	}
	t := a.tab()
	if t == nil || t.Focus == nil {
		return
	}
	old := t.Focus
	if old.terminal() == nil {
		return
	}
	if t.Host != nil {
		a.restartDesktopPane(old)
		return
	}
	cols, rows := 80, 24
	if old.terminalView() != nil {
		cols, rows = old.terminal().term.Size()
	}
	p := a.newPane(a.currentDir(), cols, rows)
	a.endPane(old, true)
	a.replacePane(old, p)
}

func (a *App) quitAndEnd() {
	a.quitting = true
	for _, p := range a.allPanes() {
		p.closed = true
	}
	c := a.client
	go func() {
		c.Shutdown()
		mygo.App.Quit()
	}()
}

func (a *App) openPalette() {
	if a.terminalHistory != nil && a.terminalHistory.open {
		a.terminalHistory.close()
	}
	a.settingsOpen = false
	a.paletteOpen, a.paletteQuery = true, ""
	a.focusReq = nil // the palette takes the focus
	a.loadPaletteSSH()
}

// shortcuts also handles tab navigation in views without a native menu,
// and Command-1, which has no menu item.
// Control-Tab is a menu accelerator so AppKit handles it before its own
// key-view navigation. The original bracket shortcuts remain aliases.
func (a *App) shortcuts(c *ui.Context) {
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		a.openSettings()
		c.Invalidate()
	}
	if a.settingsOpen || a.desktops.open || a.remoteUpdate != nil && a.remoteUpdate.open || a.terminalHistory != nil && a.terminalHistory.open || a.focusedFiles() != nil && (a.focusedFiles().expanded || a.focusedFiles().pathEditing) {
		return
	}
	if !a.paletteOpen && c.Shortcut(ui.Shift|ui.Cmd, ui.KeyE) {
		a.toggleFiles()
		c.Invalidate()
		return
	}
	if !a.paletteOpen && prefs.DesktopLayout == "sidebar" && c.Shortcut(ui.Ctrl|ui.Cmd, ui.KeyS) {
		a.toggleTabSidebar()
		c.Invalidate()
	}
	// Command-1 switches between the tab bar and an expanded sidebar.
	if !a.paletteOpen && c.Shortcut(ui.Cmd, ui.Key1) {
		if prefs.DesktopLayout == "sidebar" {
			a.setDesktopLayout("")
		} else {
			a.showTabSidebar()
		}
		c.Invalidate()
	}
	if c.Shortcut(ui.Cmd, ui.KeyF) {
		a.openFind()
		c.Invalidate()
	}
	if c.Shortcut(ui.Cmd, ui.KeyG) {
		a.findStep(1)
		c.Invalidate()
	}
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyG) {
		a.findStep(-1)
		c.Invalidate()
	}
	nextTab := c.Shortcut(ui.Ctrl, ui.KeyTab)
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyBracketRight) || nextTab {
		a.cycleTab(1)
		c.Invalidate()
	}
	prevTab := c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyTab)
	if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyBracketLeft) || prevTab {
		a.cycleTab(-1)
		c.Invalidate()
	}
}

// paletteItem is a row of the command palette: a command, or a pane to
// go to.
type paletteItem struct {
	id, title, detail, keys string
	glyph                   string
	run                     func()
}

func (a *App) paletteItems() []paletteItem {
	if query, ok := sshPaletteQuery(a.paletteQuery); ok {
		return a.sshPaletteItems(query)
	}
	var items []paletteItem
	q := strings.ToLower(strings.TrimSpace(a.paletteQuery))
	match := func(s string) bool { return q == "" || fuzzy(strings.ToLower(s), q) }
	for ti, t := range a.tabs {
		for _, p := range t.panes() {
			name, detail := p.label()
			if !match(fmt.Sprintf("%s %s tab %d", name, detail, ti+1)) {
				continue
			}
			items = append(items, paletteItem{
				id: fmt.Sprintf("pane:%d:%d", t.ID, p.ID), title: name, detail: detail, glyph: paneProgram(p).Glyph,
				keys: fmt.Sprintf("Tab %d", ti+1),
				run: func() {
					a.selectTab(ti)
					t.setFocus(p)
					a.focusReq = p
				},
			})
		}
	}
	for _, cmd := range paletteCommands {
		if cmd.Hidden || !cmd.available(a) || !match(cmd.Title) {
			continue
		}
		items = append(items, paletteItem{id: "command:" + cmd.ID, title: cmd.Title, keys: cmd.Keys, glyph: "command", run: func() { cmd.Run(a) }})
	}
	return items
}

// fuzzy reports whether the letters of q appear in s, in order.
func fuzzy(s, q string) bool {
	if strings.Contains(s, q) {
		return true
	}
	i := 0
	for _, r := range s {
		if i < len(q) && byte(r) == q[i] {
			i++
		}
	}
	return i == len(q)
}

// palette draws the command palette, over the window.
func (a *App) palette(c *ui.Context, k *colors) {
	if !a.paletteOpen {
		return
	}
	was := a.paletteOpen
	dialog := ui.DialogBase(c, &a.paletteOpen, func(backdrop, panel ui.Element) {
		backdrop.Background(k.backdrop).Justify(ui.Start).Padding(78, 0, 0, 0)
		panel.Width(560).MaxHeight(440).Radius(radiusXL).Background(k.panel).Border(1, k.panelBorder).
			Shadow(0, 24, 60, -8, k.dialogShadow).Shadow(0, 2, 6, 0, k.dialogShadowNear).Clip()
		var choices ui.ComboboxParts
		ui.Row(c).Padding(spaceL, spaceXL).Gap(spaceM).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(k.panelBorder).Children(func() {
			ui.Icon(c, icon("search")).Size(17, 17).TextColor(k.textFaint)
			choices = ui.ComboboxBase(c.Key("palette-input"), &a.paletteQuery)
			choices.Input.Label("Search commands").Grow(1).FontSize(textTitle).Placeholder("Type a command, a pane name, or ssh…").AutoFocus()
		})
		var items []paletteItem
		choices.Inline(func(list ui.Element) {
			choices.Input.Changed() // apply pending input before filtering
			items = a.paletteItems()
			list.FillWidth()
			listH := float32(min(max(len(items), 1)*34+12, 384))
			ui.Scroll(c.Key("palette-options")).Height(listH).Padding(spaceS).Children(func() {
				if len(items) == 0 {
					ui.Text(c, a.paletteEmptyMessage()).FontSize(textBody).TextColor(k.textFaint).Padding(spaceL)
				}
				for _, it := range items {
					// No Key: an option takes its click as Item returns, so
					// re-keying it afterwards would lose the click.
					row := choices.Item(it.id).Label(it.title).Height(34).Padding(0, spaceM).Gap(spaceM).Radius(radiusM).AlignItems(ui.Center).Cursor(ui.CursorPointer)
					if row.Highlighted() {
						row.Background(k.panelSel)
					} else if row.Hovered() {
						row.Background(k.hover)
					}
					row.Children(func() {
						programIcon(c, it.glyph).Size(15, 15).TextColor(k.textMuted)
						ui.Text(c, it.title).FontSize(textBody).FontWeight(500).TextColor(k.text).SingleLine().Ellipsis("…").Shrink(0.3).MinWidth(0)
						if it.detail != "" {
							ui.Text(c, it.detail).FontSize(textBody).TextColor(k.textFaint).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
						}
						ui.Spacer(c)
						if it.keys != "" {
							ui.Text(c, it.keys).FontSize(textSmall).TextColor(k.textFaint).Padding(space2XS, spaceS).Radius(radiusM).Background(k.hover).Shrink(0)
						}
					})
				}
			})
		})
		if id, ok := choices.Chosen(); ok {
			for _, it := range items {
				if it.id == id {
					a.paletteOpen = false
					it.run()
					break
				}
			}
		}
	})
	if was && (dialog.Dismissed() || !a.paletteOpen) && !a.settingsOpen {
		if t := a.tab(); t != nil && a.focusReq == nil {
			if t.Focus == nil || t.Focus.terminal() == nil || !t.Focus.terminal().find.focus {
				a.focusReq = t.Focus
			}
		}
	}
}
