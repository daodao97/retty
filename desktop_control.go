package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"retty/internal/desktopcontrol"
	"retty/internal/rex"
)

// registeredCommands includes hidden shortcuts as well as palette entries.
// Menus, keys, the palette and CLI all refer to these same objects and IDs.
func registeredCommands() []*command {
	return append(append([]*command{}, paletteCommands...), &cmdPalette, &cmdPalette2, &cmdClear)
}

func actionByID(id string) *command {
	for _, cmd := range registeredCommands() {
		if cmd.ID == id {
			return cmd
		}
	}
	return nil
}

func actionDescriptions() []desktopcontrol.Action {
	var all []desktopcontrol.Action
	for _, cmd := range registeredCommands() {
		all = append(all, desktopcontrol.Action{ID: cmd.ID, Title: cmd.Title, Shortcut: cmd.Accel, Allowed: cmd.Automation})
	}
	return all
}

func (a *App) startDesktopControl() {
	if a.control != nil {
		return
	}
	// Capture the dispatcher and window on the UI thread. Worker goroutines
	// never inspect mutable window/tree pointers until dispatched back here.
	dispatch, win := a.desktopDispatcher(), a.win
	server, err := desktopcontrol.Listen(rex.Dir(), func(ctx context.Context, req desktopcontrol.Request) (desktopcontrol.Result, error) {
		return a.handleDesktopControl(ctx, req, func(fn func()) error {
			var err error
			done := make(chan struct{}, 1)
			dispatch(func() {
				if cause := queuedControlError(ctx); cause != nil {
					err = cause
				} else if a.win != win || a.quitting {
					err = errors.New("desktop window closed")
				} else {
					fn()
				}
				done <- struct{}{}
			})
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-done:
				return err
			}
		})
	})
	if err != nil {
		log.Printf("desktop automation: %v", err)
		return
	}
	a.control = server
}

// Timer delivery may lag the socket's absolute deadline under load. Check the
// deadline itself before a queued UI action, not just context's timer callback.
func queuedControlError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func (a *App) controlPane(sid string) (*Pane, error) {
	if sid == "" {
		return nil, errors.New("action requires --target with a stable session ID")
	}
	for p := range a.hostPanes(nil) {
		if p.SID == sid && !p.closed {
			return p, nil
		}
	}
	if len(sid) >= 4 {
		var found *Pane
		for p := range a.hostPanes(nil) {
			if !p.closed && strings.HasPrefix(p.SID, sid) {
				if found != nil {
					return nil, errors.New("ambiguous session prefix; use the full ID")
				}
				found = p
			}
		}
		if found != nil {
			return found, nil
		}
	}
	return nil, fmt.Errorf("session %q is not open in this desktop", sid)
}

// onUI waits on a worker, never on the UI thread. Its closure must check the
// context before mutation, so a timed-out queued action cannot run later.
func (a *App) handleDesktopControl(ctx context.Context, req desktopcontrol.Request, onUI func(func()) error) (desktopcontrol.Result, error) {
	if req.Op == "browser" {
		if req.Browser == nil {
			return desktopcontrol.Result{}, errors.New("browser request required")
		}
		result, err := a.handleBrowserControl(ctx, *req.Browser, onUI)
		return desktopcontrol.Result{Browser: &result}, err
	}
	if req.Op == "actions" {
		return desktopcontrol.Result{Actions: actionDescriptions()}, nil
	}
	if req.Op == "action" {
		cmd := actionByID(req.Action)
		if cmd == nil {
			return desktopcontrol.Result{}, fmt.Errorf("unknown action ID %q", req.Action)
		}
		if !cmd.Automation {
			return desktopcontrol.Result{}, fmt.Errorf("action %q is available only through the GUI; CLI cannot close, restart, clear or update sessions", req.Action)
		}
		if cmd == &cmdSplitRight || cmd == &cmdSplitDown {
			req.Op, req.Direction = "split", "right"
			if cmd == &cmdSplitDown {
				req.Direction = "down"
			}
		} else {
			var actionErr error
			err := onUI(func() {
				if cmd.Target || req.SID != "" {
					p, err := a.controlPane(req.SID)
					if err != nil {
						actionErr = err
						return
					}
					a.selectControlPane(p)
				}
				cmd.Run(a)
			})
			if err == nil {
				err = actionErr
			}
			return desktopcontrol.Result{}, err
		}
	}
	if req.Op != "split" {
		return desktopcontrol.Result{}, fmt.Errorf("unknown desktop operation %q", req.Op)
	}
	if req.Direction != "right" && req.Direction != "down" {
		return desktopcontrol.Result{}, errors.New("direction must be right or down")
	}
	var parent *Pane
	var client *rex.Client
	var err error
	options := rex.CreateOptions{Cols: 80, Rows: 24}
	if req.Create != nil {
		options = *req.Create
	}
	if options.Cols < 2 || options.Cols > 1000 || options.Rows < 2 || options.Rows > 1000 {
		return desktopcontrol.Result{}, errors.New("invalid initial terminal dimensions")
	}
	errUI := onUI(func() {
		parent, err = a.controlPane(req.SID)
		if err != nil {
			return
		}
		if parent.info.Exited {
			err = errors.New("target session has exited")
			return
		}
		client = a.client
		if options.Dir == "" {
			options.Dir = parent.info.Dir
			if options.Dir == "" {
				options.Dir = parent.startDir
			}
		}
	})
	if errUI != nil {
		return desktopcontrol.Result{}, errUI
	}
	if err != nil {
		return desktopcontrol.Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return desktopcontrol.Result{}, err
	}
	// Creation can block on PTY startup; it must not block drawing or input.
	in, err := client.Create(options)
	if err != nil {
		return desktopcontrol.Result{}, err
	}
	result := desktopcontrol.Result{SID: in.ID}
	errUI = onUI(func() {
		var now *Pane
		now, err = a.controlPane(req.SID)
		if err != nil || now != parent || a.client != client {
			err = fmt.Errorf("target changed; new session %s is available through list", in.ID)
			return
		}
		// Polling may already have discovered this just-created session. Move
		// that single-pane tab into the split instead of attaching it twice.
		var p *Pane
		for existing := range a.hostPanes(nil) {
			if existing.SID == in.ID {
				if existing.Tab.Root.Pane != existing {
					err = errors.New("new session is already placed in a split")
					return
				}
				p = existing
				for i, t := range a.tabs {
					if t == p.Tab {
						a.tabs = slices.Delete(a.tabs, i, i+1)
						break
					}
				}
				break
			}
		}
		if p == nil {
			p = &Pane{ID: a.id(), SID: in.ID, info: in, startDir: options.Dir}
			a.attach(p, in.Cols, in.Rows)
		}
		a.insertControlSplit(parent, p, req.Direction == "down")
	})
	if errUI != nil {
		return result, fmt.Errorf("split placement failed; new session %s remains available: %w", in.ID, errUI)
	}
	return result, err
}

func (a *App) selectControlPane(p *Pane) {
	for i, t := range a.tabs {
		if t == p.Tab {
			a.selectTab(i)
			t.setFocus(p)
			a.focusReq = p
			return
		}
	}
}

func (a *App) insertControlSplit(parent, p *Pane, vertical bool) {
	t, n := parent.Tab, parent.Node
	a.selectControlPane(parent)
	t.Zoom = nil
	p.Tab = t
	left, right := &Node{ID: a.id(), Pane: parent, Parent: n}, &Node{ID: a.id(), Pane: p, Parent: n}
	parent.Node, p.Node = left, right
	n.Pane, n.A, n.B, n.Vertical, n.Ratio = nil, left, right, vertical, 0.5
	t.setFocus(p)
	a.focusReq = p
	a.changed()
}
