//go:build darwin || linux

package rex

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"retty/internal/atomicfile"
	"retty/internal/gitstate"
	"retty/internal/runtimeinfo"
)

// Server owns the sessions.
type Server struct {
	turnSnapshots *gitstate.SnapshotStore
	turnStarts    map[string]string // guarded by agentMu; suppress duplicate turn starts
	mu            sync.Mutex
	agentMu       sync.Mutex // serialize shared-daemon session binding and reports
	agentToken    string
	recoveryPath  string
	recovery      map[string]agentResume
	shuttingDown  func() bool // nil uses the OS detector; injectable for shutdown fixtures
	ending        bool        // explicit End All prevents late hooks from recreating records
	sessions      map[string]*session
	order         []string
	layout        json.RawMessage
	started       time.Time
	hostID        string   // computed before serving and never written again
	host          HostInfo // guarded by mu; filled in once hostDone closes
	hostDone      chan struct{}
	ln            net.Listener
	exe           string
	exeTime       time.Time
	controls      int
	idleFrom      time.Time
	quit          chan struct{}
	quitOnce      sync.Once
	attentionMu   sync.Mutex
	attentionPath string
	attentionSeen map[string]string
}

// idleTimeout is how long a server with no sessions and no app waits
// before it exits.
const idleTimeout = 10 * time.Minute

// Serve runs the server until it is shut down: it returns an error at
// once when another server already runs.
func Serve() error {
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "server.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("rex: a server already runs")
	}
	defer lock.Close()
	agentToken, err := loadAgentToken(dir)
	if err != nil {
		return err
	}
	deviceID, err := loadDeviceID(dir)
	if err != nil {
		return err
	}
	sock := SocketPath()
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	os.Chmod(sock, 0o600)
	s := &Server{
		turnSnapshots: &gitstate.SnapshotStore{Dir: filepath.Join(dir, "git-snapshots")},
		agentToken:    agentToken, hostID: hostID(deviceID),
		sessions: map[string]*session{}, started: time.Now(), ln: ln,
		hostDone: make(chan struct{}), idleFrom: time.Now(), quit: make(chan struct{}),
	}
	if err := s.loadRecovery(dir); err != nil {
		// Preserve the damaged/unknown file and keep ordinary sessions usable.
		log.Printf("rex: load Agent recovery: %v", err)
		s.recoveryPath = ""
	}
	if err := s.loadAttention(dir); err != nil {
		log.Printf("rex: load attention receipts: %v", err)
		s.attentionPath = ""
	}
	if b, err := os.ReadFile(filepath.Join(dir, "layout.json")); err == nil && json.Valid(b) {
		s.layout = b
	}
	s.exe, s.exeTime = Executable()
	go func() {
		h := hostInfo()
		s.mu.Lock()
		s.host = h
		s.mu.Unlock()
		close(s.hostDone)
	}()
	go s.watchIdle()
	go s.watchRecovery()
	go func() {
		<-s.quit
		ln.Close()
	}()
	log.Printf("rex: serving on %s (pid %d)", sock, os.Getpid())
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.quit:
				s.mu.Lock()
				all := make([]*session, 0, len(s.sessions))
				for _, ss := range s.sessions {
					all = append(all, ss)
				}
				s.mu.Unlock()
				for _, ss := range all {
					ss.kill()
				}
				os.Remove(sock)
				return nil
			default:
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		go s.handle(conn)
	}
}

func (s *Server) watchIdle() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.quit:
			return
		case <-t.C:
		}
		s.mu.Lock()
		idle := len(s.sessions) == 0 && s.controls == 0 && time.Since(s.idleFrom) > idleTimeout
		s.mu.Unlock()
		if idle {
			log.Print("rex: idle, exiting")
			s.shutdown()
			return
		}
	}
}

func (s *Server) shutdown() { s.quitOnce.Do(func() { close(s.quit) }) }

func (s *Server) handle(conn net.Conn) {
	r := bufio.NewReaderSize(conn, 64<<10)
	first, err := r.ReadBytes('\n')
	if err != nil {
		conn.Close()
		return
	}
	var a Attach
	if json.Unmarshal(first, &a) == nil && a.Op == "attach" {
		s.mu.Lock()
		ss := s.sessions[a.SID]
		s.mu.Unlock()
		if ss == nil {
			conn.Write([]byte(`{"error":"no such session"}` + "\n"))
			conn.Close()
			return
		}
		if a.Owner != "" {
			// Before the answer: a client that attached holds the lock.
			ss.lockSize(a.Owner, a.Device, a.Cols, a.Rows, conn, a.SizeLease)
			defer ss.releaseSizeAttachment(a.Owner, conn)
		}
		stateSync := a.ScreenFrames && a.ScreenSync
		answer, _ := json.Marshal(attachResponse{OK: true, ScreenFrames: a.ScreenFrames, ScreenSync: stateSync, ScreenGraphics: a.ScreenGraphics, SizeLease: a.Owner != "" && a.SizeLease})
		if _, err := conn.Write(append(answer, '\n')); err != nil {
			conn.Close()
			return
		}
		if stateSync {
			ss.attachScreenSync(conn, r, a.Cols, a.Rows, a.InputOnly, a.ScreenGraphics)
			return
		}
		// What the reader buffered past the first line is input.
		if n := r.Buffered(); n > 0 {
			b, _ := r.Peek(n)
			ss.input(b)
		}
		ss.attachScreen(conn, a.Cols, a.Rows, a.ScreenFrames, a.ScreenGraphics)
		return
	}
	s.control(conn, r, first)
}

// control serves a control connection: a request per line.
func (s *Server) control(conn net.Conn, r *bufio.Reader, first []byte) {
	s.mu.Lock()
	s.controls++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.controls--
		s.idleFrom = time.Now()
		s.mu.Unlock()
		conn.Close()
	}()
	var wmu sync.Mutex
	reply := func(res Response) {
		b, _ := json.Marshal(res)
		wmu.Lock()
		conn.Write(append(b, '\n'))
		wmu.Unlock()
	}
	line := first
	for {
		var req Request
		if err := json.Unmarshal(line, &req); err == nil {
			// Kills wait for the session to end: answer them aside.
			if req.Op == "kill" {
				go func() { reply(s.request(req)) }()
			} else {
				reply(s.request(req))
			}
		}
		var err error
		line, err = r.ReadBytes('\n')
		if err != nil {
			return
		}
	}
}

func (s *Server) request(req Request) Response {
	data, err := s.do(req)
	res := Response{ID: req.ID}
	if err != nil {
		res.Error = err.Error()
	} else if data != nil {
		res.Data, _ = json.Marshal(data)
	}
	if req.Op == "list" && res.Error == "" {
		sum := sha256.Sum256(res.Data)
		res.ETag = hex.EncodeToString(sum[:12])
		if req.ETag == res.ETag {
			res.Data = nil
		}
	}
	return res
}

func (s *Server) session(id string) (*session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss := s.sessions[id]; ss != nil {
		return ss, nil
	}
	return nil, fmt.Errorf("no session %q", id)
}

func (s *Server) do(req Request) (any, error) {
	switch req.Op {
	case "hello":
		select {
		case <-s.hostDone:
		case <-time.After(3 * time.Second):
		}
		s.mu.Lock()
		host := s.host
		s.mu.Unlock()
		host.ID = s.hostID
		return Hello{Build: runtimeinfo.Current(), Version: ProtocolVersion, PID: os.Getpid(), Started: s.started, Host: host, Exe: s.exe, ExeTime: s.exeTime, AgentRecovery: s.recoveryPath != "", Attention: s.attentionPath != "", ScreenSync: true, ScreenGraphics: true, TurnDiff: s.turnSnapshots != nil, SessionControl: true}, nil
	case "sendInput":
		ss, err := s.session(req.SID)
		if err != nil {
			return nil, err
		}
		return nil, ss.sendInput(req.Input)
	case "programStatus":
		ss, err := s.session(req.SID)
		if err != nil {
			return nil, err
		}
		return nil, ss.reportProgram(req.Status)
	case "history":
		ss, err := s.session(req.SID)
		if err != nil {
			return nil, err
		}
		return ss.history(req.HistoryRows)
	case "list":
		s.mu.Lock()
		all := make([]*session, 0, len(s.order))
		for _, id := range s.order {
			all = append(all, s.sessions[id])
		}
		s.mu.Unlock()
		infos := make([]SessionInfo, len(all))
		for i, ss := range all {
			infos[i] = ss.info()
			s.attentionMu.Lock()
			infos[i].AttentionSeen = s.attentionSeen[ss.id]
			s.attentionMu.Unlock()
		}
		return infos, nil
	case "markAttention":
		return nil, s.markAttention(req.SID, req.Notice)
	case "create":
		s.agentMu.Lock()
		defer s.agentMu.Unlock()
		if s.ending {
			return nil, errors.New("session server is ending")
		}
		o := CreateOptions{}
		if req.Create != nil {
			o = *req.Create
		}
		id := newID()
		ss, err := newSessionForServer(id, o, s.agentToken)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.sessions[id] = ss
		s.order = append(s.order, id)
		s.mu.Unlock()
		return ss.info(), nil
	case "kill":
		// Forget and unlist the session under agentMu, so a racing hook cannot
		// bind to it, but end it outside: ending may wait a second.
		s.agentMu.Lock()
		if err := s.forgetRecovery(req.SID); err != nil {
			s.agentMu.Unlock()
			return nil, err
		}
		delete(s.turnStarts, req.SID)
		s.mu.Lock()
		ss := s.sessions[req.SID]
		delete(s.sessions, req.SID)
		s.order = slices.DeleteFunc(s.order, func(id string) bool { return id == req.SID })
		if len(s.sessions) == 0 {
			s.idleFrom = time.Now()
		}
		s.mu.Unlock()
		s.agentMu.Unlock()
		if ss != nil {
			ss.kill()
		}
		return nil, nil
	case "restoreAgent":
		return s.restoreAgent(req)
	case "resize", "lockedResize", "unlockSize", "renewSize", "resync", "clear":
		ss, err := s.session(req.SID)
		if err != nil {
			return nil, err
		}
		switch req.Op {
		case "resize":
			ss.resize(req.Cols, req.Rows)
		case "lockedResize":
			if req.Owner == "" || !ss.lockedResize(req.Owner, req.Cols, req.Rows) {
				return nil, errSizeUnlocked
			}
		case "unlockSize":
			ss.unlockSizeOwned(req.Owner, req.Cols, req.Rows)
		case "renewSize":
			if !ss.renewSizeLock(req.Owner) {
				return nil, errSizeUnlocked
			}
		case "resync":
			ss.resend(true)
		case "clear":
			ss.clear()
		}
		return nil, nil
	case "agentEvent":
		return nil, s.reportAgent(req)
	case "getLayout":
		s.mu.Lock()
		l := s.layout
		s.mu.Unlock()
		if l == nil {
			return nil, nil
		}
		return l, nil
	case "setLayout":
		if !json.Valid(req.Layout) {
			return nil, errors.New("layout is not JSON")
		}
		l := bytes.Clone(req.Layout)
		s.mu.Lock()
		s.layout = l
		s.mu.Unlock()
		atomicfile.Write(filepath.Join(Dir(), "layout.json"), l)
		return nil, nil
	case "shutdown":
		s.agentMu.Lock()
		s.ending = true
		clear(s.recovery)
		err := s.saveRecovery()
		s.agentMu.Unlock()
		if err != nil {
			return nil, err
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.shutdown()
		}()
		return nil, nil
	}
	return nil, fmt.Errorf("unknown op %q", req.Op)
}

var errSizeUnlocked = errors.New("size lock released")

func newID() string {
	var b [6]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// commandOutput is the trimmed output of a command, empty when it fails.
func commandOutput(name string, args ...string) string {
	b, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// hostID identifies this machine, or this installation when the machine has
// no identity. It is cheap, so the server computes it before serving: hello
// and attention receipts must agree on it from the first request.
func hostID(deviceID string) string {
	if runtime.GOOS == "darwin" {
		for _, line := range strings.Split(commandOutput("ioreg", "-rd1", "-c", "IOPlatformExpertDevice"), "\n") {
			if _, value, ok := strings.Cut(line, `"IOPlatformUUID" = `); ok {
				return deviceFingerprint(strings.Trim(value, `"`))
			}
		}
	} else if data, err := os.ReadFile("/etc/machine-id"); err == nil {
		return deviceFingerprint(string(data))
	}
	return "installation:" + deviceID
}

// hostInfo describes this machine apart from its ID; it may take a second.
func hostInfo() HostInfo {
	h := HostInfo{}
	if u, err := user.Current(); err == nil {
		h.User = u.Username
		h.Home = u.HomeDir
	}
	out := commandOutput
	if runtime.GOOS == "darwin" {
		h.Name = out("scutil", "--get", "ComputerName")
		h.OS = "macOS " + out("sw_vers", "-productVersion")
		h.Chip = sysctlString("machdep.cpu.brand_string")
		h.Memory = sysctlUint64("hw.memsize")
		var hw struct {
			Data []struct {
				MachineName string `json:"machine_name"`
				ChipType    string `json:"chip_type"`
			} `json:"SPHardwareDataType"`
		}
		if json.Unmarshal([]byte(out("system_profiler", "SPHardwareDataType", "-json", "-detailLevel", "mini")), &hw) == nil && len(hw.Data) > 0 {
			h.Model = hw.Data[0].MachineName
			if hw.Data[0].ChipType != "" {
				h.Chip = hw.Data[0].ChipType
			}
		}
		if h.Model == "" {
			h.Model = sysctlString("hw.model")
		}
	} else {
		h.Name, _ = os.Hostname()
		h.OS = runtime.GOOS
		if b, err := os.ReadFile("/etc/os-release"); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(l, "PRETTY_NAME="); ok {
					h.OS, _ = strconv.Unquote(v)
				}
			}
		}
		h.Model = "Linux"
	}
	if h.Name == "" {
		h.Name, _ = os.Hostname()
		h.Name = strings.TrimSuffix(h.Name, ".local")
	}
	return h
}

// Spawn starts a server in the background, as a process of its own that
// outlives the app: the executable run with -server.
func Spawn() error {
	return StartBackground([]string{"-server"}, Dir(), "server.log")
}
