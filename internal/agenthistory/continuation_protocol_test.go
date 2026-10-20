package agenthistory

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func codexProtocolFixture(t *testing.T) (Session, string, string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	home := t.TempDir()
	executable := filepath.Join(home, "codex")
	script := `#!` + python + `
import json,os,sys
from pathlib import Path
home=Path(os.environ['CODEX_HOME'])
for line in sys.stdin:
 request=json.loads(line)
 if 'id' not in request:continue
 method=request['method'];p=request.get('params') or {}
 with (home/'calls.jsonl').open('a') as log:log.write(json.dumps({'method':method,'params':p,'pane_env':any(k.startswith('RETTY_') for k in os.environ)})+'\n')
 config=json.loads((home/'fixture.json').read_text())
 ids_path=home/'ids.json'
 ids=json.loads(ids_path.read_text()) if ids_path.exists() else []
 result={}
 if method=='config/read':result={'config':{'model_provider':config.get('provider'),'model':'current-model'}}
 elif method=='thread/fork':
  new_id='fork-'+str(len(ids)+1);ids.append(new_id);ids_path.write_text(json.dumps(ids))
  result={'thread':{'id':new_id},'modelProvider':p['modelProvider']}
 elif method=='thread/read':
  if p['threadId'] not in ids:
   print(json.dumps({'id':request['id'],'error':{'code':-32600,'message':'thread not loaded'}}),flush=True);continue
  result={'thread':{'id':p['threadId'],'modelProvider':config.get('provider') or 'openai'}}
 elif method not in ['initialize','thread/archive']:raise RuntimeError('unexpected method: '+method)
 print(json.dumps({'id':request['id'],'result':result}),flush=True)
`
	if err = os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, "fixture.json"), map[string]string{"provider": "after"})
	s := Session{Agent: "codex", ID: "original", Dir: home, ConfigDir: home, Provider: "before", Title: "Original"}
	return s, executable, filepath.Join(home, "retty", "continuations.json")
}

func TestCodexContinuationProviderAccountAndRepeatedClicks(t *testing.T) {
	s, executable, state := codexProtocolFixture(t)
	t.Setenv("RETTY_SESSION_ID", "must-not-impersonate-pane")
	s.ResumeError = "original account is no longer logged in"
	const clicks = 5
	ids := make(chan string, clicks)
	errs := make(chan error, clicks)
	var workers sync.WaitGroup
	for range clicks {
		workers.Go(func() {
			target, err := ContinueCodex(s, executable, s.ConfigDir, state, nil)
			ids <- target.ID
			errs <- err
		})
	}
	workers.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for id := range ids {
		if id != "fork-1" {
			t.Fatalf("duplicate or incorrect continuation: %s", id)
		}
	}
	current := s
	current.ID = "fork-1"
	current.Provider = "after"
	current.ResumeError = ""
	resumed, err := ContinueCodex(current, executable, s.ConfigDir, state, nil)
	if err != nil || resumed.ID != current.ID {
		t.Fatalf("same provider did not resume: %+v %v", resumed, err)
	}
	calls, err := os.ReadFile(filepath.Join(s.ConfigDir, "calls.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	forks := 0
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		var call struct {
			Method  string         `json:"method"`
			Params  map[string]any `json:"params"`
			PaneEnv bool           `json:"pane_env"`
		}
		if json.Unmarshal([]byte(line), &call) != nil {
			t.Fatal("invalid fixture log")
		}
		if call.PaneEnv {
			t.Fatal("helper inherited pane identity")
		}
		if call.Method == "thread/fork" {
			forks++
			if call.Params["modelProvider"] != "after" || call.Params["deferGoalContinuation"] != true || call.Params["model"] != "current-model" {
				t.Fatalf("incorrect fork settings: %v", call.Params)
			}
		}
		if strings.HasPrefix(call.Method, "turn/") {
			t.Fatal("preparation started inference")
		}
	}
	if forks != 1 {
		t.Fatalf("forked %d times", forks)
	}
	// Changing the currently logged-in account must use a different continuation
	// even when the provider has the same name.
	write(t, filepath.Join(s.ConfigDir, "fixture.json"), map[string]string{"provider": "openai"})
	write(t, filepath.Join(s.ConfigDir, "auth.json"), map[string]any{"tokens": map[string]string{"account_id": "current-account", "access_token": "fixture-private-token"}})
	s.Provider, s.AccountID = "openai", "original-account"
	accountFork, err := ContinueCodex(s, executable, s.ConfigDir, state, nil)
	if err != nil || accountFork.ID != "fork-2" || accountFork.AccountID != "current-account" {
		t.Fatalf("wrong account continuation: %+v %v", accountFork, err)
	}
	write(t, filepath.Join(s.ConfigDir, "auth.json"), map[string]any{"tokens": map[string]string{"account_id": "other-account"}})
	other, err := ContinueCodex(s, executable, s.ConfigDir, state, nil)
	if err != nil || other.ID != "fork-3" {
		t.Fatalf("account switch reused wrong fork: %+v %v", other, err)
	}
	data, _ := os.ReadFile(state)
	if strings.Contains(string(data), "fixture-private-token") {
		t.Fatal("credential saved in continuation metadata")
	}
	// A removed branch must be recreated rather than launching a broken ID.
	os.WriteFile(filepath.Join(s.ConfigDir, "ids.json"), []byte(`[]`), 0600)
	recreated, err := ContinueCodex(s, executable, s.ConfigDir, state, nil)
	if err != nil || recreated.ID != "fork-1" {
		t.Fatalf("stale branch not recreated: %+v %v", recreated, err)
	}
}
