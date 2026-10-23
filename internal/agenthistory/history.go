// Package agenthistory reads local coding-agent history without starting an
// agent inference. Native continuation preparation preserves original history;
// Retty retains only display and resume metadata.
package agenthistory

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"retty/internal/agents"
)

type Session struct {
	// Project identity groups linked worktrees; Dir remains the exact resume
	// directory. These fields are enriched by the sidebar's background scan.
	ProjectID, ProjectDir, Branch     string
	Agent, ID, Dir, Title, ConfigDir  string
	Provider, AccountID, AccountLabel string
	ResumeError                       string
	Updated                           time.Time
	named                             bool // database name takes precedence over an old JSONL index
}

func (s Session) Key() string { return s.Agent + ":" + s.ConfigDir + ":" + s.ID }

// Resume uses argv, never shell text or transcript commands. Preserve the
// configuration home that owns this conversation, including for custom homes.
func (s Session) Resume() (command, env []string, err error) {
	if s.ResumeError != "" {
		return nil, nil, errors.New(s.ResumeError)
	}
	if s.Agent != "codex" && s.Agent != "claude" {
		return nil, nil, errors.New("不支持的 Agent")
	}
	if !validID(s.ID) || !filepath.IsAbs(s.Dir) || !filepath.IsAbs(s.ConfigDir) {
		return nil, nil, errors.New("会话恢复信息无效")
	}
	st, err := os.Stat(s.Dir)
	if err != nil {
		return nil, nil, fmt.Errorf("项目目录不可用：%w", err)
	}
	if !st.IsDir() {
		return nil, nil, errors.New("项目路径不是目录")
	}
	if s.Agent == "codex" {
		command = []string{"codex", "resume", s.ID, "-C", s.Dir}
		if s.Provider != "" {
			// Encode a TOML string rather than interpolating provider text as code.
			provider, _ := json.Marshal(s.Provider)
			command = append(command, "-c", "model_provider="+string(provider))
		}
	} else {
		command = []string{"claude", "--resume", s.ID}
	}
	return command, []string{agents.ConfigEnv(s.Agent) + "=" + s.ConfigDir}, nil
}

type cachedFile struct {
	size    int64
	mod     time.Time
	session Session
}

// Reader is owned by one background worker. Unchanged transcripts are not read
// again; entries for deleted files are discarded on each scan.
type Reader struct{ files map[string]cachedFile }

func (r *Reader) Read(codexHome, claudeHome string) ([]Session, error) {
	return r.ReadSources(Sources{Homes: []Home{{Dir: codexHome}}}, claudeHome)
}

func (r *Reader) ReadSources(sources Sources, claudeHome string) ([]Session, error) {
	sources.Homes = slices.Clone(sources.Homes)
	next := make(map[string]cachedFile)
	byID := make(map[string]Session)
	var errs []error
	add := func(path, agent, home string, info fs.FileInfo) {
		cached, ok := r.files[path]
		if !ok || cached.size != info.Size() || !cached.mod.Equal(info.ModTime()) {
			cached = cachedFile{size: info.Size(), mod: info.ModTime()}
			cached.session = readSession(path, agent, home, info.ModTime())
		}
		next[path] = cached
		s := cached.session
		// Several account homes may share a symlinked history directory. The
		// parsed file is cacheable; its owning configuration home is not.
		s.ConfigDir = home
		if !validID(s.ID) || !filepath.IsAbs(s.Dir) {
			return
		}
		if old, ok := byID[s.Key()]; !ok || s.Updated.After(old.Updated) {
			byID[s.Key()] = s
		}
	}
	for i, home := range sources.Homes {
		if home.AccountID == "" {
			home.AccountID = currentAccountID(home.Dir)
			sources.Homes[i] = home
		}
		codexHome := home.Dir
		for _, sub := range []string{"sessions", "archived_sessions"} {
			root := filepath.Join(codexHome, sub)
			if resolved, err := filepath.EvalSymlinks(root); err == nil {
				root = resolved
			}
			err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					if !os.IsNotExist(err) {
						errs = append(errs, fmt.Errorf("Codex 历史：%w", err))
					}
					return nil
				}
				// Imported snapshots back native fork lineage; they are not
				// additional user conversations in the destination account.
				if d.IsDir() && strings.HasPrefix(d.Name(), "retty-import-") {
					return filepath.SkipDir
				}
				if !d.IsDir() && d.Type().IsRegular() && strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(path, ".jsonl") {
					if st, err := d.Info(); err == nil {
						add(path, "codex", codexHome, st)
					}
				}
				return nil
			})
			if err != nil {
				errs = append(errs, err)
			}
		}
		// Paginated Codex history no longer has a rollout JSONL on disk. The
		// state database is authoritative for its title, provider and activity.
		sessions, err := readCodexDatabase(codexHome)
		if err != nil {
			errs = append(errs, fmt.Errorf("Codex 历史 %s：%w", codexHome, err))
		}
		for _, s := range sessions {
			if old, ok := byID[s.Key()]; ok && s.Title == "" {
				s.Title = old.Title
			}
			byID[s.Key()] = s
		}
	}
	// Claude's top-level transcripts are projects/<encoded directory>/<id>.jsonl.
	// Do not descend into per-session subagents, which are not resumable chats.
	root := filepath.Join(claudeHome, "projects")
	projects, err := os.ReadDir(root)
	if err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("Claude Code 历史：%w", err))
	}
	for _, project := range projects {
		if !project.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, project.Name()))
		if err != nil {
			errs = append(errs, fmt.Errorf("Claude Code 项目：%w", err))
			continue
		}
		for _, file := range files {
			if file.Type().IsRegular() && strings.HasSuffix(file.Name(), ".jsonl") {
				if st, err := file.Info(); err == nil {
					add(filepath.Join(root, project.Name(), file.Name()), "claude", claudeHome, st)
				}
			}
		}
	}
	r.files = next
	names := make(map[string]map[string]string)
	for _, home := range sources.Homes {
		names[home.Dir] = codexNames(filepath.Join(home.Dir, "session_index.jsonl"))
	}
	result := make([]Session, 0, len(byID))
	for _, s := range byID {
		if s.Agent == "codex" {
			if title := names[s.ConfigDir][s.ID]; title != "" && !s.named {
				s.Title = title
			}
			var keep bool
			s, keep = sources.describe(s, byID)
			if !keep {
				continue
			}
		}
		if s.Title == "" {
			s.Title = s.ID
		}
		result = append(result, s)
	}
	slices.SortFunc(result, func(a, b Session) int {
		if n := b.Updated.Compare(a.Updated); n != 0 {
			return n
		}
		return strings.Compare(a.Key(), b.Key())
	})
	return result, errors.Join(errs...)
}

func validID(s string) bool {
	return s != "" && len(s) <= 128 && s[0] != '-' && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	}) < 0
}

func displayTitle(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

// Read bounded regions rather than whole transcripts, which can be gigabytes.
// The prefix has cwd/ID and the first prompt; the tail has recent Claude titles.
func readSession(path, agent, home string, modified time.Time) Session {
	f, err := os.Open(path)
	if err != nil {
		return Session{}
	}
	defer f.Close()
	s := Session{Agent: agent, ConfigDir: home, Updated: modified}
	var title, customTitle string
	invalid := false
	visit := func(line []byte) {
		var e struct {
			Type, CWD, SessionID, Summary, CustomTitle, AITitle string
			IsSidechain, IsMeta                                 bool
			Message                                             json.RawMessage
			Payload                                             struct {
				ID, CWD, Type, Message string
				ModelProvider          string `json:"model_provider"`
				CreatorAccountID       string `json:"creator_account_id"`
				ThreadSource           string `json:"thread_source"`
				Source                 json.RawMessage
			}
		}
		if json.Unmarshal(line, &e) != nil {
			return
		}
		if agent == "codex" {
			p := e.Payload
			if e.Type == "session_meta" {
				// Source objects identify spawned workers. Old root sessions can
				// omit source; user CLI, editor and app sessions are all included.
				if hiddenCodexSource(string(p.Source), p.ThreadSource) {
					invalid = true
				}
				s.ID, s.Dir = p.ID, p.CWD
				s.Provider, s.AccountID = p.ModelProvider, p.CreatorAccountID
			}
			if title == "" && e.Type == "event_msg" && p.Type == "user_message" {
				title = displayTitle(p.Message)
			}
		} else {
			if e.IsSidechain {
				invalid = true
			}
			if s.ID == "" && e.SessionID != "" && e.CWD != "" {
				s.ID, s.Dir = e.SessionID, e.CWD
			}
			switch e.Type {
			case "custom-title":
				customTitle = displayTitle(e.CustomTitle)
			case "ai-title":
				title = displayTitle(e.AITitle)
			case "summary":
				if title == "" {
					title = displayTitle(e.Summary)
				}
			case "user":
				if title == "" && !e.IsMeta {
					var msg struct{ Content json.RawMessage }
					if json.Unmarshal(e.Message, &msg) == nil {
						title = userText(msg.Content)
					}
				}
			}
		}
	}
	const prefix = 2 << 20
	scanLines(io.LimitReader(f, prefix), visit)
	if st, err := f.Stat(); err == nil && agent == "claude" && st.Size() > prefix {
		if _, err := f.Seek(max(prefix, st.Size()-(128<<10)), io.SeekStart); err == nil {
			b := bufio.NewReader(io.LimitReader(f, 128<<10))
			b.ReadBytes('\n') // tail starts within a record
			scanLines(b, visit)
		}
	}
	if invalid {
		return Session{}
	}
	if customTitle != "" {
		title = customTitle
	}
	s.Title = title
	return s
}

func userText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var content []struct{ Type, Text string }
		if json.Unmarshal(raw, &content) == nil {
			for _, block := range content {
				if block.Type == "text" {
					text += block.Text + " "
				}
			}
		}
	}
	// Claude's tool/command records are not conversation titles.
	if strings.HasPrefix(strings.TrimSpace(text), "<") || strings.HasPrefix(text, "[Request interrupted") {
		return ""
	}
	return displayTitle(text)
}

func scanLines(r io.Reader, visit func([]byte)) {
	b := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	skipping := false
	for {
		part, err := b.ReadSlice('\n')
		if len(line)+len(part) > 2<<20 {
			skipping = true
		}
		if !skipping {
			line = append(line, part...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if !skipping && len(line) > 0 && (err == nil || err == io.EOF) {
			visit(line)
		}
		line = line[:0]
		skipping = false
		if err != nil {
			return
		}
	}
}

func codexNames(path string) map[string]string {
	names := make(map[string]string)
	f, err := os.Open(path)
	if err != nil {
		return names
	}
	defer f.Close()
	scanLines(f, func(line []byte) {
		var e struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if json.Unmarshal(line, &e) == nil && e.ID != "" && strings.TrimSpace(e.ThreadName) != "" {
			names[e.ID] = displayTitle(e.ThreadName) // append-only, latest usable name wins
		}
	})
	return names
}
