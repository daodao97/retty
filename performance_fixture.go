package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"retty/internal/rex"
)

func configurePerformanceFixture() bool {
	path := os.Getenv("RETTY_PERF_SOCKET")
	if path == "" {
		return false
	}
	if _, err := performanceFixtureToken(rex.Dir(), path); err != nil || mygo.IsDev() {
		log.Fatal("performance fixture requires an owned directory and production build")
	}
	// Window placement and MyGo preferences also belong to the disposable run.
	mygo.App.SetPath(mygo.PathUserData, rex.Dir())
	return true
}

// The performance runner owns a fresh data directory and a private socket.
// A blocking listener adds no polling work to the idle baseline.
func (a *App) performanceFixture() {
	path := os.Getenv("RETTY_PERF_SOCKET")
	if path == "" {
		return
	}
	token, err := performanceFixtureToken(rex.Dir(), path)
	if err != nil || mygo.IsDev() {
		log.Print("performance fixture requires an owned directory and production build")
		return
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		log.Print("performance fixture: ", err)
		return
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		os.Remove(path)
		return
	}
	win := a.win
	win.OnClosed(func() { ln.Close(); os.Remove(path) })
	win.OnFocus(func() { log.Print("performance fixture gained focus") })
	win.OnBlur(func() {
		log.Printf("performance fixture lost focus (visible=%v minimized=%v)", win.IsVisible(), win.IsMinimized())
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				var req struct {
					Token string `json:"token"`
					Op    string `json:"op"`
					Value string `json:"value"`
				}
				if json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&req) != nil || subtle.ConstantTimeCompare([]byte(req.Token), []byte(token)) != 1 {
					return
				}
				if req.Op == "capture" {
					png, err := win.CapturePage()
					if err == nil {
						err = os.WriteFile(filepath.Join(rex.Dir(), "capture.png"), png, 0o600)
					}
					response := map[string]any{"captured": err == nil}
					if err != nil {
						response["error"] = err.Error()
					}
					json.NewEncoder(conn).Encode(response)
					return
				}
				result := make(chan map[string]any, 1)
				win.Update(func() {
					width, height := win.ContentSize()
					response := map[string]any{"pid": os.Getpid(), "tabs": len(a.tabs),
						"focused": win.IsFocused(), "content_size_dip": []int{width, height}}
					t := a.tab()
					if t == nil || t.Focus == nil || t.Focus.terminalView() == nil {
						response["error"] = "terminal not ready"
					} else {
						p := t.Focus
						switch req.Op {
						case "status":
						case "focus":
							mygo.App.Focus()
							win.Focus()
						case "diagnostics":
							if req.Value == "open" {
								a.openDiagnostics()
							} else if req.Value == "close" {
								a.settingsOpen = false
							} else if a.diagnostics != nil {
								response["diagnostics"] = a.diagnostics.report
								response["diagnostics_error"] = a.diagnostics.err
							}
						case "search":
							a.openFind()
							p.terminal().find.query = req.Value
							if err := p.terminal().term.SetSearch(req.Value); err != nil {
								response["error"] = err.Error()
							}
						case "preview":
							// Fixtures live under this run's data directory.
							rel, err := filepath.Rel(rex.Dir(), filepath.Clean(req.Value))
							if err != nil || !filepath.IsLocal(rel) {
								response["error"] = "preview is outside fixture directory"
								break
							}
							if a.focusedFiles() == nil {
								a.toggleFiles()
							}
							a.syncFiles()
							if a.focusedFiles() != nil {
								a.previewFile(a.focusedFiles(), req.Value, false, 1<<20)
							}
						case "quit":
							mygo.App.Quit()
						default:
							response["error"] = "unknown fixture operation"
						}
						search := p.terminal().term.SearchState()
						response["search_busy"], response["search_matches"] = search.Busy, search.Total
						if search.Err != nil {
							response["error"] = search.Err.Error()
						}
						if a.focusedFiles() != nil {
							response["preview_ready"] = !a.focusedFiles().previewLoading && a.focusedFiles().previewCode != nil
							if a.focusedFiles().err != "" {
								response["error"] = a.focusedFiles().err
							}
						}
					}
					result <- response
				})
				select {
				case response := <-result:
					json.NewEncoder(conn).Encode(response)
				case <-time.After(4 * time.Second):
				}
			}()
		}
	}()
}

func performanceFixtureToken(dir, socket string) (string, error) {
	if !filepath.IsAbs(dir) || filepath.Dir(socket) != filepath.Clean(dir) {
		return "", errors.New("socket must be in the fixture directory")
	}
	var marker struct {
		Kind  string `json:"kind"`
		Token string `json:"token"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "perf-fixture.json"))
	if err != nil || json.Unmarshal(b, &marker) != nil || marker.Kind != "retty-performance-fixture" || len(marker.Token) < 32 {
		return "", errors.New("missing performance fixture marker")
	}
	return marker.Token, nil
}
