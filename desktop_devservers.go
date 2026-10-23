package main

import (
	"fmt"
	"net/url"
	"time"

	"github.com/egoist/mygo/ui"
	"retty/internal/devserver"
	"retty/internal/rex"
	"retty/internal/terminal"
)

var devServerWorkers = make(chan struct{}, 4)

type paneDevServers struct {
	term     *terminal.Terminal
	client   *rex.Client
	urls     []devserver.URL
	selected string
	revision uint64
	busy     bool
	next     time.Time
}

func (a *App) syncDevServers(c *ui.Context) {
	if prefs.HideDevServers || a.quitting || a.win == nil && !a.devScanFixture {
		return
	}
	t := a.tab()
	if t == nil {
		return
	}
	for _, p := range t.panes() {
		if p.closed || p.browser != nil || p.term == nil {
			continue
		}
		var client *rex.Client
		if p.host != nil {
			client = p.host.client
		}
		if p.devServers == nil || p.devServers.term != p.term || p.devServers.client != client {
			p.devServers = &paneDevServers{term: p.term, client: client}
		}
		d := p.devServers
		if d.busy || p.term.OutputRevision() == d.revision {
			continue
		}
		if now := time.Now(); now.Before(d.next) {
			c.After(d.next.Sub(now))
			continue
		}
		select {
		case devServerWorkers <- struct{}{}:
		default:
			continue
		}
		d.busy = true
		d.next = time.Now().Add(time.Second)
		term, revision, host := p.term, d.revision, p.host
		update := a.desktopDispatcher()
		go func() {
			text, rev, changed := term.RecentOutput(128, revision)
			var urls []devserver.URL
			if changed {
				urls = devserver.Extract(text)
			}
			<-devServerWorkers
			update(func() {
				if p.closed || a.quitting || p.devServers != d || p.term != term || p.host != host || host != nil && host.client != client {
					return
				}
				d.busy = false
				if changed {
					d.urls, d.revision = urls, rev
					found := false
					for _, u := range urls {
						found = found || u.URL == d.selected
					}
					if !found {
						d.selected = ""
						if len(urls) > 0 {
							d.selected = urls[0].URL
						}
					}
				}
			})
		}()
	}
}

func (a *App) openDevServer(p *Pane, raw string) (*Pane, error) {
	if p == nil || p.closed || p.Tab == nil {
		return nil, fmt.Errorf("源终端已关闭")
	}
	// Repeated clicks reuse the same host/page instead of starting a shell,
	// another dev server, or another listener for the same browser.
	for _, existing := range a.allPanes() {
		if !existing.closed && existing.host == p.host && existing.browser != nil && sameBrowserPage(existing.browser.url, raw) {
			a.selectControlPane(existing)
			if existing.Tab.Zoom != nil {
				existing.Tab.Zoom = existing
			}
			return existing, nil
		}
	}
	return a.openBrowser(p, raw, p.host, false)
}

func sameBrowserPage(left, right string) bool {
	a, err := url.Parse(left)
	if err != nil {
		return false
	}
	b, err := url.Parse(right)
	if err != nil {
		return false
	}
	if a.Path == "" {
		a.Path = "/"
	}
	if b.Path == "" {
		b.Path = "/"
	}
	return a.String() == b.String()
}

func (a *App) devServerBar(c *ui.Context, k *colors, p *Pane) {
	d := p.devServers
	if prefs.HideDevServers || d == nil || len(d.urls) == 0 || p.host != nil && d.client != p.host.client {
		return
	}
	labels := make([]string, len(d.urls))
	selected := ""
	for i, u := range d.urls {
		labels[i] = u.Label
		if u.URL == d.selected {
			selected = labels[i]
		}
	}
	ui.Row(c.Key("dev-server-bar")).Height(30).FillWidth().Padding(0, 8+paneActionInset(p), 0, 10).Gap(8).AlignItems(ui.Center).Background(k.panel).Children(func() {
		ui.Icon(c, icon("globe")).Size(13, 13).TextColor(k.textMuted)
		ui.Text(c, "开发地址").FontSize(11).TextColor(k.textMuted).Shrink(0)
		if ui.Select(c, &selected, labels).Label("Detected development URLs").Grow(1).MinWidth(0).Changed() {
			for _, u := range d.urls {
				if u.Label == selected {
					d.selected = u.URL
				}
			}
		}
		if ui.Button(c, "打开").Label("Open development page beside terminal").Disabled(p.host != nil && !p.host.connected()).Clicked() {
			raw := d.selected
			a.later(c, func() {
				if _, err := a.openDevServer(p, raw); err != nil {
					a.err = err.Error()
				}
			})
		}
	})
}
