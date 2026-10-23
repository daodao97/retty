package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/browsercontrol"
	"retty/internal/remote"
	"retty/internal/rex"
)

type browserView interface {
	Native() *mygo.WebView
	LoadURL(string) error
	Eval(context.Context, string) (any, error)
	Capture() ([]byte, error)
	Focus()
	Back()
	Forward()
	Destroy()
}
type nativeBrowserView struct{ v *mygo.WebView }

func (n *nativeBrowserView) Native() *mygo.WebView  { return n.v }
func (n *nativeBrowserView) LoadURL(u string) error { return n.v.Page().LoadURL(u) }
func (n *nativeBrowserView) Eval(ctx context.Context, js string) (any, error) {
	return n.v.Page().EvalContext(ctx, js)
}
func (n *nativeBrowserView) Capture() ([]byte, error) { return n.v.CapturePage() }
func (n *nativeBrowserView) Focus()                   { n.v.Focus() }
func (n *nativeBrowserView) Back()                    { n.v.Page().GoBack() }
func (n *nativeBrowserView) Forward()                 { n.v.Page().GoForward() }
func (n *nativeBrowserView) Destroy()                 { n.v.Destroy() }

type browserPane struct {
	id, url, address, title, err      string
	view                              browserView
	loading, connecting, addressFocus bool
	epoch                             uint64
	client                            *rex.Client
	forward                           *remote.PortForward
	physical, logicalOrigin           string
	life                              context.Context
	close                             context.CancelFunc
	nav                               context.CancelFunc
	navContext                        context.Context
}
type savedBrowser struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func newBrowserState(id, raw string) (*browserPane, error) {
	u, err := browsercontrol.NormalizeURL(raw)
	if err != nil {
		return nil, err
	}
	if id == "" {
		var random [12]byte
		if _, err = rand.Read(random[:]); err != nil {
			return nil, err
		}
		id = "web-" + hex.EncodeToString(random[:])
	}
	life, cancel := context.WithCancel(context.Background())
	return &browserPane{id: id, url: u, address: u, life: life, close: cancel, addressFocus: true}, nil
}
func (b *browserPane) info(host *desktopHost) browsercontrol.Info {
	h := ""
	if host != nil {
		h = host.key
	}
	return browsercontrol.Info{ID: b.id, URL: b.url, Title: b.title, Host: h, Loading: b.loading, Error: b.err}
}
func (b *browserPane) stop() {
	b.close()
	if b.nav != nil {
		b.nav()
		b.nav = nil
	}
	if b.forward != nil {
		b.forward.Close()
		b.forward = nil
	}
	if b.view != nil {
		b.view.Destroy()
		b.view = nil
	}
	b.epoch++
}

func (a *App) newBrowserPane(id, raw string, host *desktopHost) (*Pane, error) {
	// Saved files can contain damaged or repeated IDs; repair those without
	// letting one restored pane silently become another automation target.
	if id != "" {
		valid := strings.HasPrefix(id, "web-") && len(id) <= 128
		for _, r := range id {
			valid = valid && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-')
		}
		if _, err := a.findBrowser(id); err == nil {
			valid = false
		}
		if !valid {
			id = ""
		}
	}
	b, err := newBrowserState(id, raw)
	if err != nil {
		return nil, err
	}
	return &Pane{ID: a.id(), host: host, browser: b}, nil
}
func (a *App) openBrowser(parent *Pane, raw string, host *desktopHost, down bool) (*Pane, error) {
	p, err := a.newBrowserPane("", raw, host)
	if err != nil {
		return nil, err
	}
	if parent != nil {
		if parent.closed || parent.Tab == nil || !slices.Contains(a.tabs, parent.Tab) {
			p.browser.stop()
			return nil, fmt.Errorf("target pane closed")
		}
		p.host = parent.host
		p.info.Dir, p.startDir = parent.info.Dir, parent.startDir
		a.insertControlSplit(parent, p, down)
	} else {
		t := a.singlePaneTab(p)
		at := min(a.active+1, len(a.tabs))
		a.tabs = slices.Insert(a.tabs, at, t)
		a.selectTab(at)
		a.focusReq = p
		a.changed()
	}
	if prefs.FilesOpen {
		a.toggleFiles()
	}
	return p, nil
}

func (a *App) syncBrowsers() {
	for t, p := range a.allPanes() {
		b := p.browser
		if b == nil || p.closed {
			continue
		}
		if b.view == nil && t == a.tab() {
			var err error
			if a.browserFactory != nil {
				b.view, err = a.browserFactory(p)
			} else if a.win != nil {
				b.view, err = a.makeBrowserView(p)
			}
			if err != nil {
				b.err = err.Error()
				continue
			}
			if b.view != nil {
				a.navigateBrowser(p, b.url)
			}
		}
		if b.view != nil && p.host != nil {
			_, loopback := browsercontrol.LoopbackPort(b.url)
			if loopback && b.client != p.host.client {
				a.navigateBrowser(p, b.url)
			}
		}
	}
}

func (a *App) makeBrowserView(p *Pane) (browserView, error) {
	b := p.browser
	v, err := a.win.NewWebView(mygo.WebViewOptions{BackgroundColor: "#ffffff", Page: mygo.PageOptions{DevTools: mygo.DevToolsEnabled}})
	if err != nil {
		return nil, err
	}
	n := &nativeBrowserView{v: v}
	page := v.Page()
	current := func() bool { return !p.closed && !a.quitting && p.browser == b && b.view == n }
	update := a.desktopDispatcher()
	page.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		e.PreventDefault()
		title := e.Title
		update(func() {
			if current() {
				b.title = title
				a.changed()
			}
		})
	})
	page.OnDidNavigate(func(raw string) {
		update(func() {
			if !current() || b.connecting {
				return
			}
			u := browsercontrol.RemapOrigin(raw, b.physical, b.logicalOrigin)
			if normalized, err := browsercontrol.NormalizeURL(u); err == nil {
				b.url, b.address = normalized, normalized
				a.changed()
			}
		})
	})
	page.OnDidFinishLoad(func() {
		update(func() {
			if current() && !b.connecting {
				b.loading = false
			}
		})
	})
	page.OnDidFailLoad(func(e *mygo.LoadError) {
		message, failedURL := e.Description, e.URL
		update(func() {
			if current() && !b.connecting && browsercontrol.RemapOrigin(failedURL, b.physical, b.logicalOrigin) == b.url {
				b.loading = false
				b.err = message
			}
		})
	})
	page.OnWillNavigate(func(e *mygo.NavigateEvent) {
		if _, err := browsercontrol.NormalizeURL(e.URL); err != nil {
			e.PreventDefault()
			return
		}
		if p.host != nil {
			_, loopback := browsercontrol.LoopbackPort(e.URL)
			mapped := browsercontrol.RemapOrigin(e.URL, b.physical, b.logicalOrigin)
			if loopback && mapped == e.URL {
				raw := e.URL
				e.PreventDefault()
				update(func() {
					if current() {
						a.navigateBrowser(p, raw)
					}
				})
			}
		}
	})
	page.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		update(func() {
			if current() {
				a.navigateBrowser(p, req.URL)
			}
		})
		return nil
	})
	return n, nil
}

func (a *App) navigateBrowser(p *Pane, raw string) {
	b := p.browser
	if b == nil || p.closed {
		return
	}
	u, err := browsercontrol.NormalizeURL(raw)
	if err != nil {
		b.err = err.Error()
		return
	}
	if b.nav != nil {
		b.nav()
	}
	if b.forward != nil {
		b.forward.Close()
		b.forward = nil
	}
	b.epoch++
	epoch := b.epoch
	b.url, b.address, b.err = u, u, ""
	b.loading, b.connecting = true, false
	b.physical, b.logicalOrigin = "", ""
	a.changed()
	if b.view == nil {
		return
	}
	ctx, cancel := context.WithCancel(b.life)
	b.nav = cancel
	b.navContext = ctx
	port, loopback := browsercontrol.LoopbackPort(u)
	if p.host == nil || !loopback {
		if err = b.view.LoadURL(u); err != nil {
			b.err = err.Error()
			b.loading = false
		}
		return
	}
	host := p.host
	client := host.client
	b.client = client
	if !host.connected() {
		b.err = "远端未连接，连接后重试"
		b.loading = false
		return
	}
	b.connecting = true
	update := a.desktopDispatcher()
	go func() {
		forward, err := remote.ForwardPort(ctx, client, port)
		update(func() {
			if ctx.Err() != nil || p.closed || b.epoch != epoch || host.client != client {
				if forward != nil {
					forward.Close()
				}
				return
			}
			b.connecting = false
			if err != nil {
				b.err = err.Error()
				b.loading = false
				return
			}
			b.forward = forward
			parsed, _ := url.Parse(u)
			b.logicalOrigin = parsed.Scheme + "://" + parsed.Host
			parsed.Host = forward.Address()
			b.physical = parsed.Scheme + "://" + parsed.Host
			if err = b.view.LoadURL(parsed.String()); err != nil {
				b.err = err.Error()
				b.loading = false
			}
		})
	}()
}

func (a *App) browserContent(c *ui.Context, k *colors, t *Tab, p *Pane) {
	b := p.browser
	ui.Row(c.Key("browser-toolbar")).Height(36).Padding(4, 8).Gap(4).AlignItems(ui.Center).Background(k.panel).Children(func() {
		if iconButton(c, k, "chevron-left", "浏览器后退", 24, 13).Clicked() && b.view != nil {
			b.view.Back()
		}
		if iconButton(c, k, "chevron-right", "浏览器前进", 24, 13).Clicked() && b.view != nil {
			b.view.Forward()
		}
		if iconButton(c, k, "rotate-ccw", "刷新浏览器", 24, 13).Clicked() {
			a.later(c, func() { a.navigateBrowser(p, b.url) })
		}
		input := ui.TextInput(c.Key("browser-url"), &b.address).Label("浏览器地址").Placeholder("https:// 或 localhost:端口").Grow(1).MinWidth(0).FontSize(12)
		if b.addressFocus {
			input.Focus()
			b.addressFocus = false
			if a.focusReq == p {
				a.focusReq = nil
			}
		}
		if input.Shortcut(0, ui.KeyEnter) {
			u := b.address
			a.later(c, func() {
				a.navigateBrowser(p, u)
				if b.view != nil {
					b.view.Focus()
				}
			})
		}
		if p.host != nil {
			ui.Text(c, p.host.name()).FontSize(10).TextColor(k.textMuted).SingleLine().MaxWidth(100).Ellipsis("…")
		}
		if iconButton(c, k, "x", "关闭浏览器", 24, 12).Clicked() {
			a.later(c, func() { a.closePane(p) })
		}
	})
	if b.err != "" {
		ui.Text(c, b.err).FontSize(12).TextColor(k.textMuted).Padding(8, 12).FillWidth()
	}
	if b.loading || b.connecting {
		ui.Row(c).Height(24).Padding(0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Size(12, 12)
			label := "正在加载页面…"
			if b.connecting {
				label = "连接远端开发端口…"
			}
			ui.Text(c, label).FontSize(11).TextColor(k.textMuted)
		})
	}
	if b.view != nil && b.view.Native() != nil {
		web := ui.WebView(c.Key("browser-page"), b.view.Native()).Grow(1).FillWidth().MinHeight(0).Label("浏览器页面")
		if a.focusReq == p {
			b.view.Focus()
			a.focusReq = nil
		}
		if web.Focused() && t.Focus != p {
			t.setFocus(p)
			a.changed()
		}
	} else {
		ui.Box(c).Grow(1).FillWidth().Center().Label("浏览器页面").Children(func() {
			label := "浏览器窗格"
			if b.view == nil && a.win != nil {
				label = "正在创建浏览器…"
			}
			ui.Text(c, label).TextColor(k.textMuted)
		})
	}
}

func browserTitle(b *browserPane) (string, string) {
	if strings.TrimSpace(b.title) != "" {
		return b.title, b.url
	}
	return "浏览器", b.url
}
