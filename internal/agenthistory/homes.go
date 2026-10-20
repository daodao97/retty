package agenthistory

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Home struct {
	Dir, Label, AccountID string
}

type Sources struct {
	Homes    []Home
	Accounts map[string]Home
	Bindings map[string]string
}

// DiscoverCodexSources discovers live configuration homes, not backup folders.
// Account registries contribute only labels, home paths and thread ownership;
// neither their credentials nor a provider's keys are copied into Retty state.
func DiscoverCodexSources(userHome, current string) Sources {
	s := Sources{Accounts: make(map[string]Home)}
	byDir := make(map[string]int)
	add := func(home Home) Home {
		home.Dir = canonicalHome(home.Dir)
		if home.Dir == "" {
			return home
		}
		if i, ok := byDir[home.Dir]; ok {
			if home.Label != "" {
				s.Homes[i].Label = home.Label
			}
			return home
		}
		byDir[home.Dir] = len(s.Homes)
		s.Homes = append(s.Homes, home)
		return home
	}
	add(Home{Dir: current})
	if userHome == "" {
		return s
	}
	defaultHome := filepath.Join(userHome, ".codex")
	if isCodexHome(defaultHome) {
		add(Home{Dir: defaultHome})
	}
	entries, _ := os.ReadDir(userHome)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".codex-") || strings.Contains(name, "backup") || strings.HasSuffix(name, ".bak") {
			continue
		}
		path := filepath.Join(userHome, name)
		if isCodexHome(path) {
			add(Home{Dir: path})
		}
	}
	var registry struct {
		Accounts []struct {
			AccountID string `json:"accountId"`
			CodexHome string `json:"codexHome"`
			Label     string `json:"label"`
		} `json:"accounts"`
	}
	base := filepath.Join(userHome, ".codexhost", "codex-accounts")
	if readMetadata(filepath.Join(base, "accounts.json"), &registry) == nil {
		for _, account := range registry.Accounts {
			if !filepath.IsAbs(account.CodexHome) || !isCodexHome(account.CodexHome) {
				continue
			}
			home := add(Home{Dir: account.CodexHome, Label: displayTitle(account.Label)})
			s.Accounts[account.AccountID] = home
		}
	}
	var bindings struct {
		Bindings map[string]string `json:"bindings"`
	}
	if readMetadata(filepath.Join(base, "thread-accounts.json"), &bindings) == nil {
		s.Bindings = bindings.Bindings
	}
	return s
}

func canonicalHome(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

func isCodexHome(path string) bool {
	for _, name := range []string{"config.toml", "state_5.sqlite", "session_index.jsonl"} {
		if st, err := os.Stat(filepath.Join(path, name)); err == nil && st.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// currentAccountID reads only the non-secret account identifier from auth.json.
// Token fields are discarded by the decoder and never retained in a Session.
func currentAccountID(home string) string {
	var auth struct {
		Tokens struct {
			AccountID string `json:"account_id"`
		} `json:"tokens"`
	}
	_ = readMetadata(filepath.Join(home, "auth.json"), &auth)
	return auth.Tokens.AccountID
}

func readMetadata(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(io.LimitReader(f, 8<<20)).Decode(value)
}

func (sources Sources) describe(s Session, all map[string]Session) (Session, bool) {
	for _, home := range sources.Homes {
		if home.Dir == s.ConfigDir {
			s.AccountLabel = home.Label
			if home.AccountID != "" && s.AccountID != "" && home.AccountID != s.AccountID && s.Provider == "openai" {
				s.ResumeError = "此会话属于其他 Codex 账号，请在原配置目录切换到该账号后重试"
				s.AccountLabel = "账号 " + string([]rune(s.AccountID)[:min(8, len([]rune(s.AccountID)))])
				for _, owner := range sources.Homes {
					if owner.AccountID != s.AccountID {
						continue
					}
					key := (Session{Agent: "codex", ConfigDir: owner.Dir, ID: s.ID}).Key()
					if _, exists := all[key]; exists {
						return s, false
					}
					if owner.Label != "" {
						s.AccountLabel = owner.Label
					}
				}
			}
			break
		}
	}
	// Legacy account managers can leave the same conversation in two homes.
	// Prefer the bound account's own copy if it exists; never copy or migrate it.
	if owner, ok := sources.Accounts[sources.Bindings[s.ID]]; ok && s.AccountID == "" {
		if owner.Dir != s.ConfigDir {
			key := (Session{Agent: "codex", ConfigDir: owner.Dir, ID: s.ID}).Key()
			if _, exists := all[key]; exists {
				return s, false
			}
			s.ResumeError = "此会话原属另一个 Codex 账号，原账号目录中未找到该会话；请先在 Codex 中确认账号和会话"
		}
		if owner.Label != "" {
			s.AccountLabel = owner.Label
		}
	}
	if s.AccountLabel == "" {
		s.AccountLabel = filepath.Base(s.ConfigDir)
	}
	return s, true
}

func hiddenCodexSource(source, threadSource string) bool {
	return threadSource == "subagent" || threadSource == "guardian_review" || strings.Contains(source, `"subagent"`)
}
