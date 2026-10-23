package rex

import (
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"retty/internal/agents"
)

// ProgramStatusSource identifies status reported directly by a terminal program,
// rather than an agent-specific hook. OSC 7501 revision 0.3 is defined at
// https://www.superlogical.com/rex/docs/build/program-status.
const ProgramStatusSource = "osc7501"
const programStatusReply = "\x1b]7501;?\x1b\\"

// ProgramNoticeLimiter bounds OSC-triggered attention outside the grid. State
// still updates immediately; suppressed events are not replayed on reconnect.
// Hook-based notices retain their existing behavior.
type ProgramNoticeLimiter map[string]time.Time

func (l *ProgramNoticeLimiter) Allow(key string, state AgentState, now time.Time) bool {
	if state.Source != ProgramStatusSource {
		return true
	}
	// A quick answer may legitimately be followed by completion. Limit
	// repeated notices of the same kind, rather than swallowing that result.
	key += "\x00" + state.State
	if previous := (*l)[key]; !previous.IsZero() && now.Sub(previous) < 2*time.Second {
		return false
	}
	if *l == nil {
		*l = make(ProgramNoticeLimiter)
	}
	if _, exists := (*l)[key]; !exists && len(*l) >= 256 {
		oldest := key
		var at time.Time
		for k, t := range *l {
			if at.IsZero() || t.Before(at) {
				oldest, at = k, t
			}
		}
		delete(*l, oldest)
	}
	(*l)[key] = now
	return true
}

// ProgramStatus is one OSC 7501 record. App is the explicitly reported name;
// a missing name inherits from the nearest existing ancestor when displayed.
type ProgramStatus struct {
	ID       string    `json:"id,omitempty"`
	State    string    `json:"state"`
	App      string    `json:"app,omitempty"`
	Kind     string    `json:"kind,omitempty"`
	Title    string    `json:"title,omitempty"`
	Message  string    `json:"message,omitempty"`
	Progress *int      `json:"progress,omitempty"`
	Updated  time.Time `json:"updated"`
	Revision uint64    `json:"revision"`
}

type programRecord struct {
	ProgramStatus
	order uint64
}

// The session owns both parsing and records: snapshots/reconnecting viewers
// cannot replay reports or generate duplicate feature-detection replies.
type programStatusTracker struct {
	records    map[string]programRecord
	serial     uint64
	lastReport time.Time
	state      byte
	length     int
	osc        []byte
	// atPrompt tells that OSC 133 marks an active prompt. Only an active
	// prompt needs clearing before its right-hand padding reflows.
	atPrompt bool
}

// feed observes the original PTY stream, retaining only a bounded OSC payload.
// DCS/APC/PM/SOS strings are opaque: an embedded OSC is not a status report.
// The returned count is fixed replies to write back to the PTY, not user input.
func (t *programStatusTracker) feed(data []byte, now time.Time) int {
	replies := 0
	for _, b := range data {
		if b == 0x18 || b == 0x1a { // CAN / SUB cancel an escape sequence.
			t.state = 0
			t.osc = t.osc[:0]
			continue
		}
		switch t.state {
		case 0:
			if b == 0x1b {
				t.state = 1
			}
		case 1:
			switch b {
			case ']':
				t.state, t.length, t.osc = 2, 2, t.osc[:0]
			case 'P', '_', '^', 'X':
				t.state = 4
			case 'c': // RIS; DECSTR and screen switches leave records intact.
				clear(t.records)
				t.lastReport = now
				t.state = 0
			case 0x1b:
			default:
				t.state = 0
			}
		case 2:
			t.length++
			switch b {
			case 7:
				if t.length <= 4096 && t.finish(now) {
					replies++
				}
				t.state = 0
			case 0x1b:
				t.state = 3
			default:
				if t.length <= 4096 {
					t.osc = append(t.osc, b)
				}
			}
		case 3:
			t.length++
			if b == '\\' {
				if t.length <= 4096 && t.finish(now) {
					replies++
				}
				t.state = 0
			} else {
				// ESC aborts the old OSC and starts a new escape sequence.
				t.state = 1
				replies += t.feed([]byte{b}, now)
			}
		case 4:
			if b == 0x1b {
				t.state = 5
			}
		case 5:
			if b == '\\' {
				t.state = 0
			} else if b != 0x1b {
				t.state = 4
			}
		}
	}
	return replies
}

func (t *programStatusTracker) finish(now time.Time) bool {
	body := string(t.osc)
	if mark, ok := strings.CutPrefix(body, "133;"); ok {
		if mark == "" || len(mark) > 1 && mark[1] != ';' {
			return false
		}
		switch mark[0] {
		case 'A':
			t.atPrompt = true
			t.processExit(now)
		case 'P', 'B', 'I':
			t.atPrompt = true
		case 'C':
			t.atPrompt = false
			// Starting the next shell command acknowledges previous results.
			// Merely returning to the prompt must still preserve them.
			t.acknowledge()
			if !t.lastReport.IsZero() {
				t.lastReport = now
			}
		case 'D':
			t.atPrompt = false
		}
		return false
	}
	body, ok := strings.CutPrefix(body, "7501;")
	if !ok {
		return false
	}
	if body == "?" {
		return true
	}
	r, ok := parseProgramStatus(body)
	if !ok {
		return false
	}
	t.apply(r, now)
	return false
}

// apply shares record/revision semantics between OSC and explicit CLI reports.
func (t *programStatusTracker) apply(r ProgramStatus, now time.Time) {
	t.lastReport = now
	if r.State == "clear" {
		for id := range t.records {
			if r.ID == "" || id == r.ID || strings.HasPrefix(id, r.ID+"/") {
				delete(t.records, id)
			}
		}
		return
	}
	if t.records == nil {
		t.records = make(map[string]programRecord)
	}
	previous, exists := t.records[r.ID]
	if !exists && len(t.records) == 256 {
		var oldest string
		var order uint64 = ^uint64(0)
		for id, record := range t.records {
			if record.order < order {
				oldest, order = id, record.order
			}
		}
		delete(t.records, oldest)
	}
	t.serial++
	r.Updated, r.Revision = now, t.serial
	// Text and progress updates are the same waiting/completion event.
	if exists && r.State == previous.State && r.Kind == previous.Kind && r.App == previous.App {
		r.Revision = previous.Revision
	}
	t.records[r.ID] = programRecord{ProgramStatus: r, order: t.serial}
}

func parseProgramStatus(body string) (ProgramStatus, bool) {
	var r ProgramStatus
	values := make(map[string]string)
	for _, pair := range strings.Split(body, ":") {
		key, value, found := strings.Cut(pair, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if len(key) > 16 {
			return r, false
		}
		if !found || key == "" || strings.IndexFunc(key, func(c rune) bool { return c < 'a' || c > 'z' }) >= 0 || !statusValue(value) {
			continue
		}
		// Validate every occurrence before last-value-wins replacement.
		switch key {
		case "title", "msg":
			encoded, decoded := 256, 192
			if key == "msg" {
				encoded, decoded = 2732, 2048
			}
			if len(value) > encoded {
				return r, false
			}
			text, err := base64.StdEncoding.Strict().DecodeString(value)
			if err != nil {
				text, err = base64.RawStdEncoding.Strict().DecodeString(value)
			}
			if err != nil || len(text) > decoded || !utf8.Valid(text) || strings.IndexFunc(string(text), statusControl) >= 0 {
				return r, false
			}
			value = string(text)
		case "app":
			if len(value) > 32 {
				return r, false
			}
		case "id":
			parts := strings.Split(value, "/")
			if len(value) > 128 || len(parts) > 8 {
				return r, false
			}
			for _, part := range parts {
				if len(part) > 32 {
					return r, false
				}
			}
		}
		values[key] = value
	}
	r.ID, r.State = values["id"], values["state"]
	if _, present := values["id"]; present {
		for _, segment := range strings.Split(r.ID, "/") {
			if !statusToken(segment) {
				return r, false
			}
		}
	}
	switch r.State {
	case "idle", "working", "done", "blocked", "error", "clear":
	default:
		return r, false
	}
	if statusToken(values["app"]) {
		r.App = values["app"]
	}
	r.Title, r.Message = values["title"], values["msg"]
	if r.State == "blocked" {
		switch values["kind"] {
		case "permission", "question", "auth":
			r.Kind = values["kind"]
		}
	}
	if r.State == "working" || r.State == "blocked" {
		v := values["progress"]
		if v != "" && strings.IndexFunc(v, func(c rune) bool { return c < '0' || c > '9' }) < 0 {
			if n, err := strconv.Atoi(v); err == nil && n <= 100 {
				r.Progress = &n
			}
		}
	}
	return r, true
}

func statusValue(v string) bool {
	return strings.IndexFunc(v, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.,+/=-", r))
	}) < 0
}
func statusToken(v string) bool {
	return v != "" && len(v) <= 32 && strings.IndexFunc(v, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.+-", r))
	}) < 0
}
func statusControl(r rune) bool { return r <= 0x1f || r >= 0x7f && r <= 0x9f }

// Status text stays plain text. Remove invisible formatting (including bidi
// overrides) only for presentation outside the terminal, not from the record.
func programStatusText(s string) string {
	return strings.Map(func(r rune) rune {
		if statusControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s)
}

func (t *programStatusTracker) processExit(now time.Time) {
	for id, record := range t.records {
		if record.State == "working" || record.State == "blocked" {
			delete(t.records, id)
		}
	}
	if !t.lastReport.IsZero() {
		t.lastReport = now
	}
}

func (t *programStatusTracker) acknowledge() {
	for id, r := range t.records {
		if r.State == "done" || r.State == "error" || r.State == "idle" {
			delete(t.records, id)
		}
	}
}

func (t *programStatusTracker) snapshot() []ProgramStatus {
	if len(t.records) == 0 {
		return nil
	}
	out := make([]ProgramStatus, 0, len(t.records))
	for _, r := range t.records {
		out = append(out, r.ProgramStatus)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (t *programStatusTracker) inheritedApp(r ProgramStatus) string {
	for r.App == "" && r.ID != "" {
		id := ""
		if n := strings.LastIndexByte(r.ID, '/'); n >= 0 {
			id = r.ID[:n]
		}
		r = t.records[id].ProgramStatus
		r.ID = id
	}
	return r.App
}

// agentState projects the highest-priority record into the shared desktop,
// mobile and APNs lifecycle. All records remain available in SessionInfo.
func (t *programStatusTracker) agentState(fallback string) AgentState {
	priority := map[string]int{"blocked": 5, "error": 4, "working": 3, "done": 2, "idle": 1}
	var chosen programRecord
	for _, r := range t.records {
		if priority[r.State] > priority[chosen.State] || priority[r.State] == priority[chosen.State] && r.order > chosen.order {
			chosen = r
		}
	}
	if chosen.State == "" {
		return AgentState{}
	}
	app := t.inheritedApp(chosen.ProgramStatus)
	if app == "" {
		app = fallback
	}
	if app == "" {
		app = "终端程序"
	}
	if a, ok := agents.Lookup(app); ok {
		app = a.ID
	}
	a := AgentState{ID: app, SessionID: "osc7501:" + chosen.ID, Source: ProgramStatusSource, Reason: chosen.Kind,
		Updated: chosen.Updated, Label: programStatusText(chosen.Title), Message: programStatusText(chosen.Message), Progress: chosen.Progress}
	switch chosen.State {
	case "idle":
		a.State = agents.Ready
	case "working":
		a.State = agents.Running
	case "blocked":
		a.State, a.WaitRevision = agents.Waiting, chosen.Revision
	case "done":
		a.State, a.CompletionRevision = agents.Completed, chosen.Revision
	case "error":
		a.State, a.CompletionRevision = agents.Failed, chosen.Revision
	}
	return a
}
