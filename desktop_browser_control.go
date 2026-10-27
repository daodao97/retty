package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"strings"
	"time"

	"retty/internal/browsercontrol"
)

var browserCaptureWorkers = make(chan struct{}, 2)

func (a *App) findBrowser(id string) (*Pane, error) {
	if id == "" {
		return nil, errors.New("specify a browser ID from browser list")
	}
	var found *Pane
	for _, p := range a.allPanes() {
		if p.browser() == nil || p.closed {
			continue
		}
		if p.browser().id == id {
			return p, nil
		}
		if len(id) >= 4 && strings.HasPrefix(p.browser().id, id) {
			if found != nil {
				return nil, errors.New("ambiguous browser ID prefix")
			}
			found = p
		}
	}
	if found == nil {
		return nil, errors.New("browser ID was not found")
	}
	return found, nil
}
func (a *App) browserParent(id, host string) (*Pane, error) {
	var found *Pane
	for _, p := range a.allPanes() {
		if p.closed || host != "" && (p.host == nil || p.host.key != host) {
			continue
		}
		candidate := p.identity()
		if v := p.terminal(); v != nil {
			candidate = v.SID
		}
		if p.browser() != nil {
			candidate = p.browser().id
		}
		if candidate == id {
			return p, nil
		}
		if len(id) >= 4 && strings.HasPrefix(candidate, id) {
			if found != nil {
				return nil, errors.New("ambiguous pane ID prefix")
			}
			found = p
		}
	}
	if found == nil {
		return nil, errors.New("split target is not open in this GUI")
	}
	return found, nil
}

func (a *App) handleBrowserControl(ctx context.Context, req browsercontrol.Request, onUI func(func()) error) (browsercontrol.Result, error) {
	if err := req.Validate(); err != nil {
		return browsercontrol.Result{}, err
	}
	var result browsercontrol.Result
	var opErr error
	if req.Command == "list" || req.Command == "open" {
		err := onUI(func() {
			if req.Command == "list" {
				for _, p := range a.allPanes() {
					if p.browser() != nil && !p.closed {
						result.Browsers = append(result.Browsers, p.browser().info(p.host))
					}
				}
				for _, h := range a.desktops.hosts {
					result.Hosts = append(result.Hosts, browsercontrol.Host{ID: h.key, Name: h.name(), Connected: h.connected()})
				}
				return
			}
			var parent *Pane
			var host *desktopHost
			if req.Host != "" {
				for _, h := range a.desktops.hosts {
					if h.key == req.Host {
						host = h
						break
					}
				}
				if host == nil {
					opErr = errors.New("unknown desktop host ID")
					return
				}
			}
			if req.Beside != "" {
				parent, opErr = a.browserParent(req.Beside, req.Host)
				if opErr != nil {
					return
				}
				host = parent.host
			}
			var p *Pane
			p, opErr = a.openBrowser(parent, req.URL, host, req.Direction == "down")
			if opErr == nil {
				info := p.browser().info(p.host)
				result.Browser = &info
			}
		})
		if err != nil {
			return result, err
		}
		return result, opErr
	}
	var p *Pane
	var b *browserPane
	var view browserView
	var life context.Context
	var physicalURL string
	var physicalOrigin, logicalOrigin string
	var epoch uint64
	var ready bool
	err := onUI(func() {
		p, opErr = a.findBrowser(req.ID)
		if opErr != nil {
			return
		}
		if req.Command != "status" && req.Command != "close" {
			a.selectControlPane(p)
			if p.Tab.Zoom != nil && p.Tab.Zoom != p {
				p.Tab.Zoom = p
				a.changed()
			}
			a.syncBrowsers()
		}
		b = p.browser()
		if req.Command == "navigate" {
			a.navigateBrowser(p, req.URL)
		}
		info := b.info(p.host)
		result.Browser = &info
		if req.Command == "close" {
			a.closePane(p)
			return
		}
		view, life = b.view, b.life
		ready = view != nil && !b.loading && !b.connecting
		if b.err != "" && req.Command != "status" && req.Command != "navigate" {
			opErr = errors.New(b.err)
		}
		if b.navContext != nil {
			life = b.navContext
		}
		physicalOrigin, logicalOrigin, epoch = b.physical, b.logicalOrigin, b.epoch
		physicalURL = browsercontrol.RemapOrigin(b.url, b.logicalOrigin, b.physical)
	})
	if err != nil {
		return result, err
	}
	if req.Command == "close" || req.Command == "navigate" || req.Command == "status" {
		return result, opErr
	}
	if opErr != nil {
		return result, opErr
	}
	// Waiting for native creation/remote forwarding stays off the UI thread.
	for !ready {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		err = onUI(func() {
			if p.closed {
				opErr = errors.New("browser closed")
				return
			}
			if b.err != "" {
				opErr = errors.New(b.err)
				return
			}
			if b.epoch != epoch {
				opErr = errors.New("page changed; read it again")
				return
			}
			view, life = b.view, b.life
			ready = view != nil && !b.loading && !b.connecting
			if b.navContext != nil {
				life = b.navContext
			}
			physicalOrigin, logicalOrigin, epoch = b.physical, b.logicalOrigin, b.epoch
			physicalURL = browsercontrol.RemapOrigin(b.url, b.logicalOrigin, b.physical)
		})
		if err != nil {
			return result, err
		}
		if opErr != nil {
			return result, opErr
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(life, cancel)
	defer stop()
	if req.Command == "screenshot" {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case browserCaptureWorkers <- struct{}{}:
		}
		// CapturePage may block on the native renderer; never call it on UI.
		type reply struct {
			data []byte
			err  error
		}
		done := make(chan reply, 1)
		go func() {
			defer func() { <-browserCaptureWorkers }()
			data, err := view.Capture()
			done <- reply{data, err}
		}()
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case captured := <-done:
			if captured.err != nil {
				return result, captured.err
			}
			if len(captured.data) > 8<<20 {
				return result, errors.New("browser screenshot exceeds 8 MiB")
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(captured.data))
			if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 16<<20 {
				return result, errors.New("invalid or oversized browser screenshot")
			}
			result.PNG = captured.data
			if err := onUI(func() {
				if p.closed || p.browser() != b || b.epoch != epoch {
					opErr = errors.New("page changed; capture again")
					return
				}
				info := b.info(p.host)
				result.Browser = &info
			}); err != nil {
				return browsercontrol.Result{}, err
			}
			if opErr != nil {
				return browsercontrol.Result{}, opErr
			}
			return result, nil
		}
	}
	script, err := req.Script()
	if err != nil {
		return result, err
	}
	if req.Command == "click" || req.Command == "type" {
		expected, _ := json.Marshal(physicalURL)
		script = fmt.Sprintf("(() => { if(location.href!==%s)throw new Error('page changed; read before controlling it'); return %s; })()", expected, script)
	}
	if req.Command == "wait" {
		timeout := 10 * time.Second
		if req.TimeoutMS > 0 {
			timeout = time.Duration(req.TimeoutMS) * time.Millisecond
		}
		var deadline context.CancelFunc
		ctx, deadline = context.WithTimeout(ctx, timeout)
		defer deadline()
		for {
			var ready bool
			err = onUI(func() {
				if p.closed {
					opErr = errors.New("browser closed")
					return
				}
				ready = !b.loading && !b.connecting
				view = b.view
				if b.err != "" {
					opErr = errors.New(b.err)
				}
			})
			if err != nil {
				return result, err
			}
			if opErr != nil {
				return result, opErr
			}
			if ready && view != nil {
				value, err := view.Eval(ctx, script)
				if err != nil {
					return result, err
				}
				if matched, ok := value.(bool); ok && matched {
					if err = onUI(func() {
						if p.closed || b.epoch != epoch {
							opErr = errors.New("page changed; wait again")
							return
						}
						info := b.info(p.host)
						result.Browser = &info
					}); err != nil {
						return result, err
					}
					if opErr != nil {
						return result, opErr
					}
					result.Matched = true
					return result, nil
				}
			}
			select {
			case <-ctx.Done():
				return result, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	value, err := view.Eval(ctx, script)
	if err != nil {
		return result, err
	}
	err = onUI(func() {
		if p.closed || p.browser() != b || b.epoch != epoch {
			opErr = errors.New("page changed; read it again")
			return
		}
		info := b.info(p.host)
		result.Browser = &info
	})
	if err != nil {
		return result, err
	}
	if opErr != nil {
		return result, opErr
	}
	if req.Command == "read" {
		data, err := json.Marshal(value)
		if err != nil || len(data) > 256<<10 {
			return result, errors.New("browser document exceeds response limit")
		}
		var doc browsercontrol.Document
		if err = json.Unmarshal(data, &doc); err != nil {
			return result, err
		}
		doc.URL = browsercontrol.RemapOrigin(doc.URL, physicalOrigin, logicalOrigin)
		// SPA/history and fragment changes are not navigation events on every
		// native backend. A successful read refreshes the logical saved URL too.
		if normalized, err := browsercontrol.NormalizeURL(doc.URL); err == nil {
			if err = onUI(func() {
				if p.closed || b.epoch != epoch {
					opErr = errors.New("page changed; read it again")
					return
				}
				if b.url != normalized {
					b.url, b.address = normalized, normalized
					a.changed()
				}
				info := b.info(p.host)
				result.Browser = &info
			}); err != nil {
				return result, err
			}
			if opErr != nil {
				return result, opErr
			}
		}
		result.Document = &doc
	} else {
		result.Matched = true
	}
	return result, nil
}
