package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/browsercontrol"
)

func TestBrowserAddressEnterNavigatesTypedURL(t *testing.T) {
	previous := prefs
	prefs = settings{FontSize: defaultFontSize}
	t.Cleanup(func() { prefs = previous })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	view := &fixtureBrowserView{}
	a.browserFactory = func(*Pane) (browserView, error) { return view, nil }
	p, err := a.openBrowser(a.tab().Focus, "about:blank", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.endPane(p, false) })
	tt.Frame()
	if err := tt.Click("浏览器地址"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("localhost:5173/preview?q=entered")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if p.browser().url != "http://localhost:5173/preview?q=entered" || len(view.urls) != 2 || view.urls[1] != p.browser().url {
		t.Fatalf("Enter did not navigate the address: address=%q url=%q loads=%v", p.browser().address, p.browser().url, view.urls)
	}
}

type fixtureBrowserView struct {
	urls      []string
	destroyed int
	eval      func(context.Context, string) (any, error)
}

func (v *fixtureBrowserView) Native() *mygo.WebView { return nil }
func (v *fixtureBrowserView) LoadURL(u string) error {
	v.urls = append(v.urls, u)
	return nil
}
func (v *fixtureBrowserView) Eval(c context.Context, s string) (any, error) {
	return v.eval(c, s)
}
func (v *fixtureBrowserView) Capture() ([]byte, error) { return nil, errors.New("not native") }
func (v *fixtureBrowserView) Focus()                   {}
func (v *fixtureBrowserView) Back()                    {}
func (v *fixtureBrowserView) Forward()                 {}
func (v *fixtureBrowserView) Destroy()                 { v.destroyed++ }

func TestBrowserSplitPersistenceAndHiddenLifecyclePreservesTerminal(t *testing.T) {
	a, tt := newTestApp(t)
	parent := a.tab().Focus
	view := &fixtureBrowserView{}
	a.browserFactory = func(*Pane) (browserView, error) { return view, nil }
	p, err := a.openBrowser(parent, "localhost:5173/app?q=1#hash", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.endPane(p, false) })
	tt.Frame()
	if p.browser().view != view || len(view.urls) != 1 || p.directory() != parent.terminal().info.Dir {
		t.Fatal("browser not initialized or lost project", view.urls, p.directory())
	}
	if ps := len(slices.Collect(a.hostPanes(nil))); ps != 1 {
		t.Fatal("browser entered PTY reconciliation", ps)
	}
	other, err := a.openBrowser(nil, "about:blank", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.endPane(other, false) })
	// The hidden page keeps its instance and navigation, while ui.WebView owns
	// visibility. It must not be recreated as tabs change.
	a.selectTab(0)
	tt.Frame()
	if len(view.urls) != 1 || view.destroyed != 0 {
		t.Fatal("hidden browser reloaded/destroyed", view)
	}
	a.tab().Root.Ratio = .37
	a.tab().Zoom = p
	a.tab().setFocus(p)
	saved := a.snapshot()
	raw, _ := json.Marshal(saved)
	if err = a.client.SetLayout(raw); err != nil {
		t.Fatal(err)
	}
	before, _ := a.client.List()
	restored := &App{client: a.client, hello: a.hello}
	if !restored.restore() {
		t.Fatal("browser layout not restored")
	}
	t.Cleanup(func() {
		for _, q := range restored.allPanes() {
			restored.endPane(q, false)
		}
	})
	got, err := restored.findBrowser(p.browser().id)
	if err != nil || got.browser().url != p.browser().url || got.directory() != parent.terminal().info.Dir || got.Tab.Focus != got || got.Tab.Zoom != got || got.Tab.Root.Ratio != .37 || !got.Tab.Root.Vertical {
		t.Fatal("browser layout identity, geometry or focus lost", got, err)
	}
	if len(restored.tabs) != len(saved.Tabs) {
		t.Fatal("browser tabs changed across restore")
	}
	if _, err := restored.findBrowser(parent.terminal().SID); err == nil {
		t.Fatal("terminal ID resolved as browser")
	}
	a.closePane(p)
	if view.destroyed != 1 || p.browser().life.Err() == nil || parent.closed {
		t.Fatal("browser close did not own only its WebView", view.destroyed, parent.closed)
	}
	after, _ := a.client.List()
	if len(before) != 1 || len(after) != 1 || before[0].ID != after[0].ID || before[0].PID != after[0].PID || after[0].Exited {
		t.Fatal("browser lifecycle changed terminal process", before, after)
	}
}

func TestBrowserCloseCancelsPendingDOMOperation(t *testing.T) {
	a, tt := newStaticTestApp(t)
	parent := a.tab().Focus
	entered := make(chan struct{}, 1)
	v := &fixtureBrowserView{eval: func(ctx context.Context, _ string) (any, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	a.browserFactory = func(*Pane) (browserView, error) { return v, nil }
	p, err := a.openBrowser(parent, "about:blank", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	p.browser().loading = false
	queue := make(chan func(), 8)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := a.handleBrowserControl(ctx, browsercontrol.Request{Command: "wait", ID: p.browser().id, Text: "never"}, func(fn func()) error {
			ack := make(chan struct{})
			select {
			case queue <- func() { fn(); close(ack) }:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-ack:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		done <- err
	}()
ready:
	for {
		select {
		case fn := <-queue:
			fn()
		case <-entered:
			break ready
		case <-ctx.Done():
			t.Fatal("DOM operation did not start")
		}
	}
	a.closePane(p)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("closed browser returned stale DOM success", err)
		}
	case <-ctx.Done():
		t.Fatal("browser close left DOM worker pending")
	}
	if v.destroyed != 1 || parent.closed {
		t.Fatal("close crossed pane lifetime", v.destroyed, parent.closed)
	}
}

func TestBrowserReadWaitsForForwardingBeforeSavingLogicalURL(t *testing.T) {
	a, tt := newStaticTestApp(t)
	parent := a.tab().Focus
	evaluated := make(chan struct{}, 1)
	v := &fixtureBrowserView{eval: func(context.Context, string) (any, error) {
		evaluated <- struct{}{}
		return map[string]any{"url": "http://127.0.0.1:49152/app#state", "title": "Ready", "text": "remote page"}, nil
	}}
	a.browserFactory = func(*Pane) (browserView, error) { return v, nil }
	p, err := a.openBrowser(parent, "http://localhost:5173/app", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer a.endPane(p, false)
	tt.Frame()
	p.browser().connecting = true
	p.Tab.Zoom = parent
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	queue := make(chan func(), 8)
	type reply struct {
		result browsercontrol.Result
		err    error
	}
	done := make(chan reply, 1)
	go func() {
		res, err := a.handleBrowserControl(ctx, browsercontrol.Request{Command: "read", ID: p.browser().id}, func(fn func()) error {
			ack := make(chan struct{})
			select {
			case queue <- func() { fn(); close(ack) }:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-ack:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
		done <- reply{res, err}
	}()
	select {
	case fn := <-queue:
		fn()
	case <-ctx.Done():
		t.Fatal("read did not dispatch")
	}
	if p.Tab.Focus != p || p.Tab.Zoom != p {
		t.Fatal("browser automation left its target hidden behind a zoomed terminal")
	}
	select {
	case <-evaluated:
		t.Fatal("read evaluated the old page while forwarding")
	default:
	}
	if p.browser().url != "http://localhost:5173/app" {
		t.Fatal("pending logical URL was replaced")
	}
	p.browser().physical, p.browser().logicalOrigin = "http://127.0.0.1:49152", "http://localhost:5173"
	p.browser().loading, p.browser().connecting = false, false
	for {
		select {
		case fn := <-queue:
			fn()
		case r := <-done:
			if r.err != nil || r.result.Document == nil || r.result.Document.URL != "http://localhost:5173/app#state" || p.browser().url != r.result.Document.URL {
				t.Fatal("lost logical SPA URL", r.result, r.err)
			}
			return
		case <-ctx.Done():
			t.Fatal("ready browser never returned DOM")
		}
	}
}
