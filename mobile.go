package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"retty/internal/remote"
	"retty/internal/rex"
	"retty/internal/terminal"
)

type mobileApp struct {
	automations                        mobileAutomations
	preview                            mobilePreview
	screen                             mobileScreen
	usage                              mobileUsage
	live                               mobileLiveActivity
	approval                           *mobileApproval
	terminalHistory                    *terminalHistory
	readReceipts                       readReceiptState
	win                                *mygo.Window
	startup                            *ui.Startup
	screenActive                       bool
	keepScreenAwake                    func(string, bool) func()
	releaseScreenAwake                 func()
	client                             *rex.Client
	closeTunnel                        func()
	cancel                             context.CancelFunc
	generation                         int
	hello                              rex.Hello
	sessions                           []rex.SessionInfo
	term                               *terminal.Terminal
	selectionMenuTerm                  *terminal.Terminal
	selectionMenuDismissed             bool
	stream                             *sessionViewStream
	selected                           rex.SessionInfo
	link, error                        string
	history                            []desktopRecent
	historySelection                   map[string]bool
	recentSessions                     []mobileRecentSession
	historyEpoch                       int
	home                               bool
	pollCancel                         context.CancelFunc
	resumeCheckID                      uint64
	busy, scanning                     bool
	resumeLink, resumeSID              string
	storage                            chan func()
	store                              *mygo.SecureStore
	scroll                             ui.ScrollState
	navigation                         *ui.Router
	navigationPage                     string
	keyboardMore                       bool
	voice                              mobileVoice
	voiceEnabled                       bool
	voicePreferenceTouched             bool
	background                         bool
	presence                           map[string]desktopPresence
	presenceCancel                     context.CancelFunc
	presenceEpoch                      int
	reconnecting                       bool
	retryAttempt                       int
	retryTimer                         *time.Timer
	sessionPreferences                 map[string]mobileSessionPreference
	titleFolderPosition                string
	titleFolderPreferenceTouched       bool
	preferenceEpoch                    int
	editingSession                     string
	editingOpen                        bool
	editingName                        string
	editingPinned                      bool
	endingSession                      rex.SessionInfo
	endingOpen                         bool
	closingSession                     string
	closedSessions                     map[string]bool
	agentPrevious                      map[string]rex.SessionInfo
	programNoticeLimiter               rex.ProgramNoticeLimiter
	notificationDenied                 bool
	agentNotify                        func(mobileAgentNotice)
	pendingDesktop, pendingSession     string
	pushDeviceID, pushToken, pushError string
	pushDisabled                       bool
	pushRequesting                     bool
	pushSnapshot                       atomic.Pointer[rex.DeviceInfo]
	noticeReceipts                     map[string]time.Time
	noticeReceiptsLoaded               bool
	preferenceTouched                  map[string]bool
	imagePasteBusy                     bool
	imagePasteCancel                   context.CancelFunc
	imagePasteEpoch                    int
	imagePastePost                     func(func()) // main-thread fixture; nil uses MyGo
	keyboardLatch                      ui.ModifierLatch
	connectionIssue                    *mobileConnectionIssue
	connectionDetailsOpen              bool
	connectionDiagnostics              []remote.DiagnosticReport
	connectionDiagnosticsOpen          bool
	connectionDiagnosticsCopied        bool
	sessionSettingsOpen                bool
	deviceNameOpen                     bool
	deviceNameDraft                    string
	deviceNameTouched                  map[string]bool
	// sizeLock, a setting, makes an opened session take the PTY's size
	// (and the desktop's pane show it) while the phone session is active.
	// lockLost tells that a window did, for the open session.
	sizeLock, lockLost, lockSeen bool
	lockOwner                    string
	lockSuspended                bool
	lockRelease                  <-chan struct{}
}

func mobileMain() {
	if dir, err := mygo.App.Path(mygo.PathUserData); err == nil {
		os.Setenv("RETTY_DIR", dir)
	}
	registerFonts()
	m := &mobileApp{storage: connectionStorage(), sizeLock: true, startup: &ui.Startup{SkipTransition: true}}
	m.keepScreenAwake = mygo.Power.KeepAwake
	storeName := "desktop-connection"
	if os.Getenv("RETTY_UI_TEST") == "1" {
		storeName += "-ui-tests"
	}
	m.store, _ = mygo.NewSecureStore(storeName, mygo.SecureStoreOptions{})
	mygo.App.OnLifecycleChanged(func(state mygo.LifecycleState) {
		m.screenActive = state == mygo.LifecycleActive
		if os.Getenv("RETTY_DEBUG_TUNNEL") == "1" {
			log.Printf("Retty lifecycle: %v", state)
		}
		m.invalidate()
	})
	mygo.App.OnOpenURL(m.openMobileURL)
	mygo.App.OnDidBecomeActive(func() { m.registerSystemPush() })
	mygo.App.OnNotification(func(event mygo.NotificationEvent) {
		if event.Clicked {
			if id := event.Data["event"]; id != "" {
				m.rememberNotice(id)
				m.refreshPushSnapshot()
				m.syncPushRegistration()
			}
			m.openNotifiedSession(event.Data["desktop"], event.Data["session"])
		}
	})
	mygo.App.WhenReady(func() {
		m.win = mygo.NewWindow(mygo.WindowOptions{Title: "Retty", Width: 390, Height: 844, BackgroundColor: "light-dark(#f5f6f8, #111315)", Content: ui.View(m.view)})
		m.invalidate()
		m.loadSessionPreferences()
		m.loadTitleFolderPreference()
		m.loadReadReceipts()
		m.setupPush()
		m.setupLiveActivity()
		historyEpoch := m.historyEpoch
		m.storage <- func() {
			// Until this loads, tunnels use ephemeral keys, as before.
			secret, err := m.store.Get("tunnel-secret")
			if errors.Is(err, mygo.ErrSecretNotFound) || err == nil && len(secret) != 32 {
				secret = make([]byte, 32)
				rand.Read(secret)
				err = m.store.Set("tunnel-secret", secret)
			}
			if err == nil {
				remote.SetClientSecret(secret)
				remote.SetKeySlotFile(filepath.Join(rex.Dir(), "client-key-slot"))
			}
			var history []desktopRecent
			var recentSessions []mobileRecentSession
			var diagnostics []remote.DiagnosticReport
			if saved, err := m.store.Get("connection-diagnostics-v1"); err == nil && len(saved) <= 65536 {
				json.Unmarshal(saved, &diagnostics)
			}

			if saved, err := m.store.Get("recent-sessions"); err == nil {
				json.Unmarshal(saved, &recentSessions)
			}
			sizeLock := true
			if saved, err := m.store.Get("size-lock"); err == nil {
				sizeLock = string(saved) == "1"
			}
			voiceEnabled := false
			if saved, err := m.store.Get("voice-input-enabled"); err == nil {
				voiceEnabled = string(saved) == "1"
			}
			history = readDesktopHistory(m.store)
			mygo.RunOnMain(func() {
				m.restoreConnectionDiagnostics(diagnostics)
				m.sizeLock = sizeLock
				m.applyLoadedVoicePreference(voiceEnabled)
				if m.historyEpoch == historyEpoch {
					m.applyLoadedConnectionHistory(history, recentSessions, historyEpoch)
					if m.pendingSession != "" {
						desktop, sid := m.pendingDesktop, m.pendingSession
						m.pendingDesktop, m.pendingSession = "", ""
						m.openNotifiedSession(desktop, sid)
					}
					m.invalidate()
				}
				m.startupLoaded()
			})
		}
	})
	mygo.App.OnDidEnterBackground(m.enterBackground)
	mygo.App.OnWillEnterForeground(m.enterForeground)
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func (m *mobileApp) invalidate() {
	m.syncScreenAwake()
	m.syncLiveActivity()
	if m.win != nil {
		m.win.Invalidate()
	}
}

// save writes a Keychain entry on the storage goroutine, so slow Keychain
// calls never block a frame. failed, if any, runs on the main thread.
func (m *mobileApp) save(key string, data []byte, failed func()) {
	if m.store == nil || m.storage == nil {
		return
	}
	m.storage <- func() {
		if m.store.Set(key, data) != nil && failed != nil {
			mygo.RunOnMain(failed)
		}
	}
}

func (m *mobileApp) detach() {
	if m.terminalHistory != nil {
		m.terminalHistory.close()
		m.terminalHistory = nil
	}
	m.cancelSpeech(true)
	m.releaseSizeLock()
	m.clearKeyboardModifiers()
	m.cancelImagePaste()
	m.keyboardMore = false
	if m.term != nil {
		m.term.Close()
		m.term = nil
	}
	m.stream = nil
	m.selected = rex.SessionInfo{}
	m.lockOwner, m.lockSuspended, m.lockSeen = "", false, false
	m.syncScreenAwake()
	mygo.App.DismissKeyboard()
}

func (m *mobileApp) disconnect(forget bool) {
	m.closePreview()
	m.closeAutomations()
	m.automations = mobileAutomations{epoch: m.automations.epoch}
	if forget {
		m.stopLiveActivity()
		m.saveLivePreference()
	}
	m.historySelection = nil
	m.stopRecentPresence()
	m.stopPolling()
	m.home = true
	m.generation++
	m.reconnecting = false
	m.retryAttempt = 0
	if m.retryTimer != nil {
		m.retryTimer.Stop()
		m.retryTimer = nil
	}
	m.agentPrevious = nil
	m.editingOpen = false
	m.sessionSettingsOpen = false
	m.deviceNameOpen = false
	m.endingOpen, m.closingSession = false, ""
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.detach()
	m.closeConnectionAfterSizeRelease()
	m.busy = false
	m.sessions = nil
	m.resumeLink, m.resumeSID = "", ""
	m.connectionIssue, m.connectionDetailsOpen = nil, false
	if forget {
		m.link, m.resumeLink, m.resumeSID = "", "", ""
		m.historyEpoch++
		m.history = nil
		m.recentSessions = nil
		m.presence = nil
		if m.store != nil && m.storage != nil {
			m.storage <- func() { m.store.Delete("recent"); m.store.Delete("history"); m.store.Delete("recent-sessions") }
		}
	}
	m.invalidate()
}

func (m *mobileApp) connect(raw string) {
	addr, err := remote.ParseLink(raw)
	if err != nil {
		trace := remote.NewDiagnostics(false, 1, 0)
		err = trace.Measure(remote.StageLink, func() error { return &remote.ConnectionError{Kind: remote.InvalidLink, Cause: err} })
		m.recordConnectionDiagnostic(trace.Finish(err))
		m.connectionIssue = connectionIssueFor(err)
		m.error = ""
		m.invalidate()
		return
	}
	link := remote.Link(addr)
	m.historySelection = nil
	if m.link == link && m.connectionUsable() {
		m.detach()
		m.home, m.error = false, ""
		m.invalidate()
		return
	}
	if m.link == link && (m.busy || m.reconnecting) {
		m.home = false
		m.invalidate()
		return
	}
	m.disconnect(false)
	m.home = false
	m.link = link
	m.resumeLink, m.resumeSID = "", ""
	if m.background {
		m.resumeLink = link
		m.reconnecting = true
		m.invalidate()
		return
	}
	m.startConnection(false)
}

func (m *mobileApp) startConnection(recovering bool) {
	if m.background || m.busy {
		return
	}
	m.busy, m.error = true, ""
	generation := m.generation
	timeout := 30 * time.Second
	if recovering {
		timeout = 12 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	m.cancel = cancel
	link := m.link
	trace := remote.NewDiagnostics(recovering, m.retryAttempt, timeout)
	device := mobileDevice()
	if snapshot := m.pushSnapshot.Load(); snapshot != nil {
		device = *snapshot
	}
	go func() {
		// Quality is not shown on the phone, but its background discovery
		// moves a relayed tunnel to a direct path when one becomes possible.
		client, closeTunnel, hello, sessions, err := remote.OpenConnection(ctx, link, device, trace, &remote.Quality{})
		report := trace.Finish(err)
		cancel()
		mygo.RunOnMain(func() {
			// Ignore attempts canceled by navigation, backgrounding or a new scan.
			if generation == m.generation {
				m.recordConnectionDiagnostic(report)
			}
			if generation != m.generation || err != nil {
				remote.CloseConnection(client, closeTunnel)
				if generation == m.generation {
					m.connectionFailed(err, recovering)
				}
				return
			}
			m.client, m.closeTunnel, m.hello = client, closeTunnel, hello
			m.reconnecting, m.retryAttempt = false, 0
			m.connectionIssue, m.connectionDetailsOpen = nil, false
			currentDesktop := desktopRecent{Link: m.link, Name: m.hello.Host.Name, ID: m.hello.Host.ID, OS: desktopPlatform(m.hello.Host)}
			m.migrateRecentDesktop(currentDesktop)
			m.history = mergeDesktopHistory([]desktopRecent{currentDesktop}, m.history)
			m.updateSessions(sessions, !recovering)
			m.syncPushRegistration()
			m.busy, m.error, m.cancel = false, "", nil
			if m.presence == nil {
				m.presence = make(map[string]desktopPresence)
			}
			m.presence[m.link] = desktopPresence{state: desktopOnline, checked: time.Now()}
			history, _ := json.Marshal(m.history)
			m.save("history", history, func() {
				if m.generation == generation {
					m.error = "已连接；无法保存连接记录，下次启动请重新扫码"
					m.invalidate()
				}
			})
			m.resumeSelected()
			m.invalidate()
			m.startPolling(client, generation)
		})
	}()
}

func (m *mobileApp) poll(ctx context.Context, client *rex.Client, generation int) {
	remote.WatchConnection(ctx, client, m.pushSnapshot.Load, func(sessions []rex.SessionInfo, err error) {
		mygo.RunOnMain(func() {
			if ctx.Err() != nil || m.background || m.generation != generation || m.client != client {
				return
			}
			if err != nil {
				m.connectionLost()
				return
			}
			m.updateSessions(sessions, false)
			for _, s := range sessions {
				if s.ID == m.selected.ID {
					if m.term != nil && m.stream != nil && !m.stream.HasScreenSize() && (s.Cols != m.selected.Cols || s.Rows != m.selected.Rows) {
						m.selected = s
						m.stream.geometry(s.Cols, s.Rows)
						m.stream.replace(m.sessionStream(s.ID, false))
					} else {
						m.selected = s
						m.followSizeLock(s)
					}
				}
			}
			m.invalidate()
		})
	}, nil)
}

func (m *mobileApp) scan() {
	if m.scanning || m.busy {
		return
	}
	m.stopRecentPresence()
	m.scanning = true
	m.error = ""
	generation := m.generation
	go func() {
		raw, err := mygo.Scanner.ScanCode(context.Background(), mygo.ScanOptions{Prompt: "扫描 Retty 桌面端的二维码", CancelLabel: "取消"})
		mygo.RunOnMain(func() {
			m.scanning = false
			if m.generation != generation {
				m.invalidate()
				return
			}
			if err != nil {
				m.error = scanError(err)
			} else if raw != "" {
				m.connect(raw)
			}
			m.invalidate()
		})
	}()
}

// scanError explains a failed scan; closing the scanner is not a failure.
func scanError(err error) string {
	switch {
	case errors.Is(err, mygo.ErrScanCanceled):
		return ""
	case errors.Is(err, mygo.ErrUnsupported):
		return "请在 iPhone 上扫码，或粘贴桌面连接码"
	case errors.Is(err, mygo.ErrCameraDenied):
		return "请在系统设置中允许 Retty 使用相机，或粘贴连接码"
	case errors.Is(err, mygo.ErrCameraUnavailable):
		return "相机不可用，请粘贴桌面连接码"
	}
	return "暂时无法打开相机"
}

// mobileDevice names this phone to desktops.
func mobileDevice() rex.DeviceInfo {
	d := mygo.App.Device()
	return rex.DeviceInfo{Name: d.Name, OS: strings.TrimSpace(d.System + " " + d.Version)}
}

func (m *mobileApp) openSession(s rex.SessionInfo) {
	if m.client == nil {
		return
	}
	if m.automations.open {
		m.closeAutomations()
	}
	if m.selected.ID != s.ID {
		// A window's unlock holds for the session it released only.
		m.lockLost = false
	}
	m.detach()
	var stream *sessionViewStream
	locking := m.locksSize()
	m.lockSeen = false
	stream = newSessionViewStream(m.sessionStream(s.ID, locking), func() {
		mygo.RunOnMain(func() {
			if m.stream == stream && !stream.hasTransport() {
				if m.closingSession == m.selected.ID {
					return // The requested close is handled by its control response.
				}
				if m.selected.Exited {
					stream.Close()
					return
				}
				m.connectionLost()
			}
		})
	})
	stream.geometry(s.Cols, s.Rows)
	options := terminal.Options{Conn: stream, FixedCols: max(s.Cols, 1), FixedRows: max(s.Rows, 1), ReflowView: true}
	if locking {
		// The program draws for this view, as in a desktop pane.
		options = terminal.Options{Conn: stream}
	}
	options.Font, options.Theme, options.DarkTheme = terminal.Font{Family: termFont.Family, Size: 13, LineHeight: 1.2}, lightTerm, darkTerm
	options.AdaptiveColors, options.OptionAsAlt, options.SelectOnDrag, options.CopyRawText = true, true, true, true
	options.ActiveCursor, options.InputContext, options.OnPaste = true, true, m.pasteClipboard
	options.OpenLinksOnTap = true
	options.SmoothScroll = true
	if os.Getenv("RETTY_DEBUG_SCROLL") == "1" {
		options.OnRenderEvent = func(event terminal.RenderEvent) {
			if strings.HasPrefix(event.Kind, "scroll_transition_") {
				log.Printf("Retty scroll: %s elapsed=%s", event.Kind, event.Duration)
			}
		}
	}
	options.OnSubmit = m.dismissKeyboard
	term, err := terminal.New(options)
	if err != nil {
		stream.Close()
		m.error = "无法打开终端：" + err.Error()
		return
	}
	m.term, m.stream, m.selected, m.error = term, stream, s, ""
	m.resumeSID = ""
	m.rememberSession(s)
	m.readResult(s)
	m.invalidate()
}

func (m *mobileApp) create() {
	if m.client == nil || m.busy || m.reconnecting || m.connectionIssue != nil || m.background {
		return
	}
	m.busy, m.error = true, ""
	client, generation, directory := m.client, m.generation, m.hello.Host.Home
	go func() {
		session, err := client.Create(rex.CreateOptions{Dir: directory, Cols: 48, Rows: 24})
		mygo.RunOnMain(func() {
			if m.generation != generation {
				return
			}
			m.busy = false
			if err != nil {
				m.error = "创建失败：" + err.Error()
			} else {
				m.sessions = append(m.sessions, session)
				m.openSession(session)
			}
			m.invalidate()
		})
	}()
}

func mobileSessionTitle(s rex.SessionInfo) string {
	prog := programOf(sessionProgramName(s))
	if title := programTitle(s.Title, prog); title != "" {
		return title
	}
	if s.Program != "" {
		if prog.Name != "" {
			return prog.Name
		}
		return s.Program
	}
	if s.Shell != "" {
		return filepath.Base(s.Shell)
	}
	return "终端"
}

func (m *mobileApp) view(c *ui.Context) {
	m.syncScreenAwake()
	theme := connectionTheme(c.Theme())
	c.SetTheme(&theme)
	c.Root().Background(theme.Background)
	if m.startup != nil {
		m.startup.View(c, func() { mobileLaunchView(c) }, func() { m.contentView(c) })
	} else {
		m.contentView(c)
	}
}

func (m *mobileApp) contentView(c *ui.Context) {
	m.syncNavigation()
	m.navigation.InteractiveBack = (!m.busy || m.reconnecting) && !m.scanning
	// 让开设备自己的 chrome（状态栏、挖孔、手势条）。
	//
	// mygo 把窗口内边距从 Java 的 WindowInsets 算好（DIP）交给 Go，但**不会
	// 自动应用**——它是给视图读的 API，不是布局的默认行为。所以这里要把
	// Context.SafeArea() 自己套上，否则内容从屏幕左上角开始画，压在状态栏下。
	//
	// 只在这一层套，不套进 terminalView：终端是满屏内容，让开手势条会平白
	// 少两行，且终端自己处理滚动。
	safe := c.SafeArea()
	ui.Column(c).Fill().Padding(safe.Top, safe.Right, safe.Bottom, safe.Left).Children(func() {
		m.navigation.View(c, func(page *ui.Route) {
			page.Page().Background(c.Theme().Background)
			switch page.Path() {
			case "/sessions/terminal":
				m.terminalView(c)
			case "/sessions":
				m.sessionsView(c)
			default:
				m.connectView(c)
			}
		})
	})
	m.sessionEditor(c)
	m.sessionSettingsDialog(c)
	m.sessionEndDialog(c)
	m.connectionDialog(c)
	m.connectionDiagnosticsDialog(c)
	m.previewDialog(c)
	m.screenDialog(c)
	m.usageDialog(c)
	m.automationsDialog(c)
	m.liveActivityDialog(c)
	m.terminalHistory.view(c)
	m.syncApproval(c)
	if page := m.navigation.Path(); page != m.navigationPage {
		// A completed edge gesture changes history. Release resources only
		// after the new page has built; a cancelled preview keeps them alive.
		switch page {
		case "/connect":
			m.goHome()
		case "/sessions":
			m.resumeSID = ""
			m.detach()
		}
		m.navigationPage = page
		// Closed terminal views cannot be revisited through forward history.
		m.navigation.Reset("/connect")
		if page == "/sessions" {
			m.navigation.Push(page)
		}
		c.Invalidate()
	}
}

// Keep asynchronous connection/session state and the navigation stack in sync.
func (m *mobileApp) syncNavigation() {
	page := "/connect"
	if !m.home && (m.client != nil || m.reconnecting) {
		page = "/sessions"
	}
	if m.term != nil {
		page = "/sessions/terminal"
	}
	if m.navigation == nil {
		m.navigation = ui.NewRouter("/connect")
		// Edge drags still preview both pages. Ordinary page changes are
		// immediate so a detached terminal is never painted while leaving.
		m.navigation.Transition = ui.TransitionNone
	}
	if page == m.navigationPage {
		return
	}
	switch page {
	case "/connect":
		m.navigation.Reset(page)
	case "/sessions":
		m.navigation.Reset("/connect")
		m.navigation.Push(page)
	default:
		if m.navigation.Path() != "/sessions" {
			m.navigation.Reset("/connect")
			m.navigation.Push("/sessions")
		}
		m.navigation.Push(page)
	}
	m.navigationPage = page
}

// Keep the full touch target while letting the title sit beside the visible
// chevron. Header text passes pointer events through the overlapping area.
func mobileBackButton(c *ui.Context) ui.Element {
	button := ui.ButtonBase(c.Key("mobile-back")).Label("返回").Role(ui.RoleButton).Size(44, 44).Margin(0, -20, 0, 0).Justify(ui.Start).Radius(radiusL)
	if button.Pressed() {
		button.Background(c.Theme().SurfacePressed)
	}
	return button.Children(func() {
		ui.Icon(c, icon("chevron-left")).Size(24, 24).TextColor(c.Theme().Accent)
	})
}

func (m *mobileApp) header(c *ui.Context, title string, back func(), terminalPage bool) {
	status := ""
	if terminalPage {
		status = screenFreshness(c, m.stream)
		if m.needsRecovery() {
			status = m.recoveryStatus()
		}
	}
	ui.Row(c).Height(52).Padding(0, spaceM).AlignItems(ui.Center).Children(func() {
		if back != nil {
			if mobileBackButton(c).Clicked() {
				back()
			}
		} else {
			ui.Box(c).Size(44, 44).Shrink(0)
		}
		ui.Column(c).Grow(1).MinWidth(0).PassThrough().Children(func() {
			align := ui.Center
			if back != nil {
				align = ui.Start
			}
			size := float32(17)
			if status != "" {
				size = 15
			}
			ui.Text(c, title).FontSize(size).Bold().TextAlign(align).FillWidth().SingleLine().Ellipsis("…").PassThrough()
			if status != "" {
				label := "画面连接状态"
				if m.needsRecovery() {
					label = "正在重连"
				}
				ui.Text(c, status).Label(label).FontSize(textCaption).TextColor(c.Theme().TextMuted).TextAlign(align).FillWidth().SingleLine().Ellipsis("…").PassThrough()
			}
		})
		if terminalPage {
			if mobileIconAction(c, "查看电脑屏幕", "monitor").Disabled(!m.connectionUsable()).Clicked() {
				c.Blur()
				m.openScreen()
			}
			if mobileIconAction(c, "会话工具", "ellipsis").Clicked() {
				m.live.tools = true
				c.Blur()
			}
			m.approvalAction(c)
		}
		if terminalPage && m.term != nil && m.needsRecovery() {
			if ui.ButtonBase(c).Label("连接恢复操作").Role(ui.RoleButton).Size(44, 44).Children(func() {
				ui.Text(c, "•••").FontSize(mobileCallout).TextColor(c.Theme().TextMuted)
			}).Clicked() {
				m.connectionDetailsOpen = true
			}
		} else if !terminalPage {
			if mobileIconAction(c, "订阅用量汇总", "activity").Clicked() {
				m.openUsage()
			}
		}
	})
}

// errorView leaves connection problems to connectionFeedback on the session
// list and to the header on the terminal page.
func (m *mobileApp) errorView(c *ui.Context) {
	if m.error != "" && !m.degraded() {
		ui.Text(c.Key("connection-status"), m.error).TextColor(c.Theme().Danger).FontSize(mobileSubhead).LineHeight(1.4).Margin(0, spaceXL, spaceL, spaceXL)
	}
}

func (m *mobileApp) connectView(c *ui.Context) {
	m.refreshRecentPresence(c)
	m.header(c, "Retty", nil, false)
	ui.Scroll(c).Grow(1).MinHeight(0).FillWidth().HideScrollbars().Padding(spaceXL).Gap(spaceXL).Children(func() {
		ui.Text(c, "电脑上的 Retty · 右上角连接按钮").FontSize(textBody).TextColor(c.Theme().TextMuted).FillWidth()
		if ui.PrimaryButton(c, "").Label("扫码连接桌面").Role(ui.RoleButton).Height(48).FillWidth().Disabled(m.busy || m.scanning || m.reconnecting).Children(func() {
			ui.Icon(c, icon("scan-line")).Size(20, 20)
			ui.Text(c, "扫码连接桌面").FontSize(mobileCallout)
		}).Clicked() {
			m.scan()
		}
		m.connectionFeedback(c)
		if !m.reconnecting && m.busy {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Text(c, "正在连接桌面…").Label("正在连接桌面").FontSize(mobileSubhead).TextColor(c.Theme().TextMuted).Grow(1)
				if mobileTextAction(c, "取消连接", "取消").Clicked() {
					m.disconnect(false)
				}
			})
		}
		ui.Column(c).FillWidth().Children(func() {
			mobileListSectionHeader(c, "最近连接", func() {
				if len(m.history) > 0 {
					label, glyph := "清除连接记录", "trash-2"
					if m.historySelection != nil {
						label, glyph = "确认清除连接记录", "check"
					}
					if mobileIconAction(c, label, glyph).Disabled(m.busy || m.scanning || m.reconnecting).Clicked() {
						if m.historySelection == nil {
							m.historySelection = make(map[string]bool)
						} else {
							m.clearSelectedDesktopHistory()
						}
					}
				}
			})
			mobileListGroup(c).Children(func() {
				if len(m.history) == 0 {
					mobileListEmpty(c, "连接过的桌面会显示在这里")
				}
				for i, entry := range m.history {
					entry := entry
					if i > 0 {
						mobileListDivider(c)
					}
					status := m.presence[entry.Link].state.label()
					label := "重新连接 " + entry.displayName()
					if m.isConnectedDesktop(entry) {
						status, label = "已连接", "打开桌面 "+entry.displayName()
					}
					selecting := m.historySelection != nil
					checked := m.historySelection[entry.Link]
					if selecting {
						label = "选择连接 " + entry.displayName()
					}
					row := mobileListRow(c, "desktop-"+entry.Link, label, 56).Value(status).Disabled(m.busy || m.scanning || m.reconnecting)
					if selecting {
						row.Role(ui.RoleCheckBox).Checked(checked)
					}
					row.Children(func() {
						if selecting {
							mobileHistoryCheckbox(c, checked)
						} else {
							platform := desktopPlatformProgram(entry.OS)
							mobileListIcon(c, platform.Glyph, colorsOf(c).iconMuted).Role(ui.RoleImage).Label(platform.Name + " icon")
						}
						mobileListText(c, mobileListTextOptions{Title: entry.displayName()})
						ui.Row(c).Gap(spaceXS).Shrink(0).AlignItems(ui.Center).Children(func() {
							color := c.Theme().TextMuted
							if m.presence[entry.Link].state == desktopOnline {
								color = colorsOf(c).busy.Mix(color, 0.25)
							}
							ui.Box(c).Size(6, 6).Radius(3).Background(color)
							ui.Text(c, status).Label("设备状态 " + entry.displayName() + " " + status).FontSize(textSmall).TextColor(c.Theme().TextMuted)
						})
						if !selecting {
							mobileListChevron(c)
						}
					})
					if row.Clicked() {
						if selecting {
							m.historySelection[entry.Link] = !checked
							c.Invalidate()
						} else {
							m.openDesktop(entry)
						}
					}
				}
			})
		})
		m.recentSessionsView(c)
		if m.error != "" {
			ui.Text(c.Key("connection-status"), m.error).TextColor(c.Theme().Danger).FontSize(mobileSubhead).LineHeight(1.4)
		}
		ui.Text(c, "通过 Tailcat 加密连接").FontSize(textSmall).TextColor(c.Theme().TextMuted).TextAlign(ui.Center).FillWidth()
	})
}

func (m *mobileApp) sessionsView(c *ui.Context) {
	ui.Row(c).Height(52).Padding(0, spaceM).AlignItems(ui.Center).Children(func() {
		if mobileBackButton(c).Clicked() {
			m.goHome()
		}
		ui.Row(c).Grow(1).Gap(spaceM).AlignItems(ui.Center).PassThrough().Children(func() {
			ui.Text(c, "会话").FontSize(mobileHeadline).Bold().PassThrough()
			ui.Text(c, fmt.Sprint(len(m.sessions))).FontSize(textBody).TextColor(c.Theme().TextMuted).PassThrough()
		})
		if mobileIconAction(c, "查看电脑屏幕", "monitor").Disabled(!m.connectionUsable()).Clicked() {
			m.openScreen()
		}
		if mobileIconAction(c, "查看自动化", "clock").Disabled(m.degraded()).Clicked() {
			m.openAutomations()
		}
		if ui.ButtonBase(c).Label("新建会话").Disabled(m.busy || m.reconnecting || m.connectionIssue != nil).Role(ui.RoleButton).Size(44, 44).Children(func() {
			if m.busy && !m.reconnecting {
				ui.Spinner(c).Size(18, 18).TextColor(c.Theme().Accent)
			} else {
				ui.Icon(c, icon("plus")).Size(23, 23).TextColor(c.Theme().Accent)
			}
		}).Clicked() {
			m.create()
		}
	})
	ui.Row(c).FillWidth().Height(44).Padding(0, spaceM, 0, spaceXL).Gap(spaceM).AlignItems(ui.Center).Children(func() {
		statusColor := colorsOf(c).busy.Mix(colorsOf(c).textMuted, 0.3)
		status := "桌面已连接"
		if m.degraded() {
			statusColor, status = colorsOf(c).attention, "桌面连接已中断"
		}
		ui.Box(c).Label(status).Size(6, 6).Shrink(0).Radius(3).Background(statusColor)
		ui.Text(c, m.connectedDevice().displayName()).FontSize(textBody).TextColor(c.Theme().TextMuted).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
		if ui.ButtonBase(c).Label("会话列表设置").Role(ui.RoleButton).Size(44, 44).Children(func() {
			ui.Icon(c, icon("settings-2")).Size(17, 17).TextColor(c.Theme().TextMuted)
			if m.pushError != "" && !m.pushDisabled {
				ui.Box(c).Absolute().Right(9).Top(9).Size(5, 5).Radius(3).Background(colorsOf(c).attention)
			}
		}).Clicked() {
			m.sessionSettingsOpen = true
		}
	})
	ui.Scroll(c.Key("mobile-sessions")).Grow(1).MinHeight(0).FillWidth().TrackScroll(&m.scroll).HideScrollbars().Padding(spaceM, spaceXL, spaceXL, spaceXL).Gap(spaceL).Children(func() {
		m.connectionFeedback(c)
		m.errorView(c)
		m.sessionList(c)
	})
}

func (m *mobileApp) terminalView(c *ui.Context) {
	m.header(c, m.sessionTitle(m.selected), func() { m.resumeSID = ""; m.detach() }, true)
	// System browser keyboard/inset changes must not resize the hidden PTY.
	if m.preview.busy {
		ui.Text(c, "开发页面预览中…").FontSize(mobileSubhead).Padding(spaceXL).TextColor(c.Theme().TextMuted)
		ui.Box(c).Grow(1)
		return
	}
	if m.term == nil {
		c.Invalidate()
		return
	}
	m.term.SetInputEnabled(!m.reconnecting && !m.background && (m.stream == nil || m.stream.inputReady()))
	m.errorView(c)
	if m.selected.Exited {
		ui.Text(c, "会话已结束").FontSize(textBody).Padding(spaceS, spaceXL).TextColor(c.Theme().TextMuted)
	}
	if m.imagePasteBusy {
		ui.Text(c, "正在粘贴图片…").FontSize(textBody).Padding(spaceS, spaceXL).TextColor(c.Theme().TextMuted)
	}
	var element ui.Element
	// Paste progress/error banners change the preceding siblings. Keep this
	// container stable so its focused terminal retains the software keyboard.
	ui.Box(c.Key("mobile-terminal-viewport")).Grow(1).MinHeight(0).FillWidth().Padding(spaceXS).Background(c.Theme().Background).Children(func() {
		keyboard := ui.KeyboardText
		if m.keyboardLatch.Active() != 0 {
			keyboard = ui.KeyboardASCII
		}
		element = terminal.View(c, m.term).Key("mobile-terminal").Fill().InputOptions(ui.InputOptions{Keyboard: keyboard, Return: ui.ReturnSend, Correction: ui.CorrectionOff, Capitalization: ui.CapitalizeNone, Dismiss: ui.KeyboardDismissOnDrag}).InputModifiers(m.keyboardLatch.Active(), m.consumeKeyboardModifiers).InputAccessory(m.keyboardActions(), func(id string) { m.keyboardAction(c.Services(), id) })
	})
	if !element.Focused() {
		m.clearKeyboardModifiers()
	}
	// Non-iOS previews use the same actions with Go controls. On iPhone,
	// UIKit owns their keyboard-attached view and the full reading viewport.
	if runtime.GOOS != "ios" && element.Focused() {
		m.keyboardPreview(c)
	}
	if m.reconnecting || m.stream != nil && !m.stream.inputReady() {
		c.Blur()
	}
	m.selectionMenu(c, element)
	m.voiceView(c, element)
	m.voiceOverlay(c, element.Focused())
}

func (m *mobileApp) selectionMenu(c *ui.Context, element ui.Element) {
	if m.selectionMenuTerm != m.term {
		m.selectionMenuTerm, m.selectionMenuDismissed = m.term, false
	}
	anchor, ok := m.term.SelectionAnchor()
	if !ok {
		// A new selection gesture temporarily hides its anchor while dragging.
		m.selectionMenuDismissed = false
		return
	}
	if m.background {
		m.selectionMenuDismissed = true
	}
	if m.selectionMenuDismissed {
		if element.PressedOutside() {
			m.term.ClearSelection()
		}
		return
	}
	bounds, viewport := element.Bounds(), c.Root().Bounds()
	x := max(8, min(bounds.X+anchor.X-74, viewport.W-156))
	y := bounds.Y + anchor.Y - 52
	if y < bounds.Y {
		y = bounds.Y + anchor.Y + anchor.H + 8
	}
	y = max(0, min(y, viewport.H-48))
	ui.Overlay(c, func() {
		menu := ui.Row(c.Key("mobile-selection-menu")).Role(ui.RolePopup).Absolute().Left(x).Top(y).Size(148, 44).KeepFocus().Radius(radiusL).Background(c.Theme().Surface).Shadow(0, 2, 12, 0, colorsOf(c).popoverShadow).Children(func() {
			if ui.ButtonBase(c).Label("复制").Role(ui.RoleButton).KeepFocus().Height(44).Grow(1).Children(func() { ui.Text(c, "复制").FontSize(mobileBody) }).Clicked() {
				if text, ok := m.term.SelectedText(); ok {
					c.WriteClipboard(text)
				}
				m.selectionMenuDismissed = true
			}
			if ui.ButtonBase(c).Label("全选").Role(ui.RoleButton).KeepFocus().Height(44).Grow(1).Children(func() { ui.Text(c, "全选").FontSize(mobileBody) }).Clicked() {
				m.term.SelectAll()
			}
		})
		if menu.PressedOutside() || menu.OverlayShortcut(0, ui.KeyEscape) {
			m.selectionMenuDismissed = true
			m.term.ClearSelection()
		}
	})
}
