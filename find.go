package main

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type paneFind struct {
	open, focus bool
	query       string
	err         error
}

func (a *App) openFind() {
	if a.focusedFiles() != nil {
		if a.focusedFiles().previewCode != nil {
			a.focusedFiles().previewCode.Run(editor.Find)
			return
		}
		a.focusedFiles().filterFocus = true
		return
	}
	t := a.tab()
	if t == nil || t.Focus == nil || t.Focus.terminalView() == nil {
		return
	}
	p := t.Focus
	if !p.terminal().find.open {
		p.terminal().find.err = p.terminal().term.SetSearch(p.terminal().find.query)
	}
	p.terminal().find.open, p.terminal().find.focus = true, true
	a.settingsOpen = false
	a.paletteOpen = false
	a.focusReq = nil
}

func (a *App) closeFind(p *Pane) {
	p.terminal().find.open, p.terminal().find.focus = false, false
	if p.terminalView() != nil {
		p.terminal().term.SetSearch("")
	}
	if t := a.tab(); t != nil {
		t.setFocus(p)
		a.focusReq = p
	}
}

func (a *App) findStep(direction int) {
	if a.focusedFiles() != nil {
		if a.focusedFiles().previewCode != nil {
			command := editor.FindNext
			if direction < 0 {
				command = editor.FindPrevious
			}
			a.focusedFiles().previewCode.Run(command)
		}
		return
	}
	if t := a.tab(); t != nil && t.Focus != nil && t.Focus.terminalView() != nil {
		p := t.Focus
		if !p.terminal().find.open {
			a.openFind()
			return
		}
		p.terminal().term.SearchNext(direction)
	}
}

// findBar belongs to its pane, so switching tabs or focusing another pane
// never searches a different session's output by accident.
func (a *App) findBar(c *ui.Context, k *colors, t *Tab, p *Pane) {
	bar := ui.Row(c.Key("find")).Height(36).Padding(0, 8+paneActionInset(p), 0, 12).Gap(6).
		AlignItems(ui.Center).Background(k.panel).BorderWidth(0, 0, 1, 0).BorderColor(k.panelBorder).
		Children(func() {
			ui.Icon(c, icon("search")).Size(14, 14).TextColor(k.textFaint).Shrink(0)
			in := ui.TextInputBase(c, &p.terminal().find.query).Label("Find in Terminal").Placeholder("Find in terminal…").
				Grow(1).MinWidth(24).FontSize(13).TextColor(k.text)
			in.HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind != ui.InputKeyDown || ev.Key != ui.KeyEnter || (ev.Mods != 0 && ev.Mods != ui.Shift) {
					return false
				}
				direction := 1
				if ev.Mods == ui.Shift {
					direction = -1
				}
				p.terminal().term.SearchNext(direction)
				c.Invalidate()
				return true
			})
			if p.terminal().find.focus {
				in.Focus()
				p.terminal().find.focus = false
			}
			if in.Focused() && t.Focus != p {
				t.setFocus(p)
				a.changed()
			}
			if in.Changed() {
				p.terminal().find.err = p.terminal().term.SetSearch(p.terminal().find.query)
			}
			if in.Shortcut(0, ui.KeyEscape) {
				a.closeFind(p)
				c.Invalidate()
			}
			if in.Shortcut(0, ui.KeyDown) {
				p.terminal().term.SearchNext(-1)
			} else if in.Shortcut(0, ui.KeyUp) {
				p.terminal().term.SearchNext(1)
			}
			state := p.terminal().term.SearchState()
			status := ""
			if p.terminal().find.err != nil || state.Err != nil {
				status = "Search unavailable"
			} else if p.terminal().find.query != "" {
				if state.Total == 0 {
					status = "No matches"
				} else {
					status = fmt.Sprintf("%d/%d", state.Current, state.Total)
				}
				if state.Busy {
					status = "Searching…"
				}
			}
			// Output and navigation already invalidate the view. Only an
			// unfinished incremental scan needs another scheduled frame.
			if state.Busy {
				c.After(100 * time.Millisecond)
			}
			ui.Text(c, status).FontSize(11).TextColor(k.textFaint).Shrink(0).SingleLine().Role(ui.RoleStatus)
			prev := iconButton(c, k, "chevron-up", "Find Previous", 24, 14).Focusable().Disabled(state.Total == 0).Tooltip("Previous match  ⇧Enter / ⇧⌘G")
			if prev.Clicked() {
				p.terminal().term.SearchNext(-1)
				in.Focus()
			}
			next := iconButton(c, k, "chevron-down", "Find Next", 24, 14).Focusable().Disabled(state.Total == 0).Tooltip("Next match  Enter / ⌘G")
			if next.Clicked() {
				p.terminal().term.SearchNext(1)
				in.Focus()
			}
			if iconButton(c, k, "x", "Close Search", 24, 14).Tooltip("Close search  Esc").Clicked() {
				a.closeFind(p)
				c.Invalidate()
			}
		})
	if bar.Shortcut(0, ui.KeyEscape) {
		a.closeFind(p)
		c.Invalidate()
	}
}
