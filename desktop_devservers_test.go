package main

import (
	"testing"

	"retty/internal/rex"
)

func TestDevelopmentURLDiscoveryOpensBesideAndReusesSameHost(t *testing.T) {
	beforePrefs := prefs
	prefs.HideDevServers = false
	t.Cleanup(func() { prefs = beforePrefs })
	t.Setenv("RETTY_DIR", t.TempDir())
	a, tt := newStaticTestApp(t)
	a.devScanFixture = true
	p := a.tab().Focus
	p.term.Feed([]byte("\x1b[H\x1b[2JLocal: \x1b[32mhttp://localhost:5173/\x1b[0m\r\nListening on 0.0.0.0:3000\r\n"))
	waitFor(t, tt, "detected development URLs", func() bool { return p.devServers != nil && len(p.devServers.urls) == 2 })
	saveSettingsImage(t, tt, "development-addresses")
	if err := tt.Click("Open development page beside terminal"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	var browser *Pane
	for _, candidate := range a.allPanes() {
		if candidate.browser != nil {
			browser = candidate
		}
	}
	if browser == nil || browser.host != p.host || browser.Tab != p.Tab || browser.browser.url != "http://localhost:3000" {
		t.Fatal("wrong browser host/location", browser)
	}
	defer a.endPane(browser, false)
	// Native navigation adds the origin's trailing slash after loading.
	browser.browser.url += "/"
	if same, err := a.openDevServer(p, "http://localhost:3000"); err != nil || same != browser || len(a.tab().panes()) != 2 {
		t.Fatal("repeated open duplicated pane", same, err)
	}
	h := &desktopHost{key: "other-host"}
	p.host, p.Tab.Host = h, h
	other, err := a.openDevServer(p, browser.browser.url)
	if err != nil || other == browser || other.host != h || other.browser.url != browser.browser.url {
		t.Fatal("borrowed local browser for a remote URL", other, err)
	}
	defer a.endPane(other, false)
}

func TestDevelopmentDiscoveryDiscardsPreviousConnectionResults(t *testing.T) {
	a, tt := newStaticTestApp(t)
	a.devScanFixture = true
	p := a.tab().Focus
	p.term.Feed([]byte("http://localhost:4000/\r\n"))
	waitFor(t, tt, "first URL", func() bool { return p.devServers != nil && len(p.devServers.urls) == 1 })
	previous := p.devServers
	p.host = &desktopHost{client: &rex.Client{}}
	p.Tab.Host = p.host
	tt.Frame()
	if p.devServers == previous {
		t.Fatal("host switch retained discovery identity")
	}
	// Disabling the feature hides the row without changing its terminal.
	old := prefs
	prefs.HideDevServers = true
	defer func() { prefs = old }()
	tt.Frame()
	if _, ok := tt.Find("Detected development URLs"); ok {
		t.Fatal("disabled discovery row still present")
	}
}
