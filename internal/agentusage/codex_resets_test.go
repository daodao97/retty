package agentusage

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Only the fixture account and the operation's opaque idempotency values are
// exchanged. The real Codex CLI, credentials and reset credits are never used.
func runResetHelper(mode string) {
	dec, enc := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	consumed := false
	for {
		var req struct {
			ID     int               `json:"id"`
			Method string            `json:"method"`
			Params map[string]string `json:"params"`
		}
		// initialize and account/read carry non-string fields, so decode the
		// request envelope separately from the consume parameters.
		var raw struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if dec.Decode(&raw) != nil {
			return
		}
		req.ID, req.Method = raw.ID, raw.Method
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
		case "initialized":
			continue
		case "account/read":
			result = map[string]any{"account": map[string]string{"type": "chatgpt", "email": "fixture@example.invalid"}}
		case "account/rateLimits/read":
			id := "fixture"
			if mode == "reset-switched" {
				id = "other"
			}
			used, count := 80, 2
			if consumed {
				used, count = 0, 1
			}
			result = map[string]any{"accountId": id, "rateLimits": map[string]any{"planType": "pro", "primary": map[string]any{"usedPercent": used, "windowDurationMins": 300}}, "rateLimitResetCredits": map[string]any{"availableCount": count}}
			if mode == "reset-refresh-failed" && consumed {
				_ = enc.Encode(map[string]any{"id": req.ID, "error": map[string]string{"message": "fixture read failure"}})
				continue
			}
		case "account/rateLimitResetCredit/consume":
			if consumed || mode == "reset-switched" {
				os.Exit(8)
			}
			if json.Unmarshal(raw.Params, &req.Params) != nil || req.Params["idempotencyKey"] == "" {
				os.Exit(9)
			}
			f, err := os.OpenFile(os.Getenv("USAGE_RESET_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(10)
			}
			_ = json.NewEncoder(f).Encode(req.Params)
			f.Close()
			consumed = true
			outcome := os.Getenv("USAGE_RESET_OUTCOME")
			if outcome == "" {
				outcome = "reset"
			}
			if mode == "reset-ambiguous" {
				_ = enc.Encode(map[string]any{"id": req.ID, "error": map[string]string{"message": "fixture ambiguous result"}})
				continue
			}
			result = map[string]string{"outcome": outcome}
		default:
			os.Exit(11)
		}
		if enc.Encode(map[string]any{"id": req.ID, "result": result}) != nil {
			return
		}
	}
}

func TestCodexResetCountRemainsAuthoritativeWithPartialDetails(t *testing.T) {
	r, err := readCodex(context.Background(), helper(t, "codex-resets"), t.TempDir())
	if err != nil || r.Resets == nil || r.Resets.Count != 3 || len(r.Resets.Credits) != 2 || r.Resets.Credits[0].ID != "first" {
		t.Fatalf("partial reset details changed count or ordering: %+v %v", r.Resets, err)
	}
	r, err = readCodex(context.Background(), helper(t, "codex"), t.TempDir())
	if err != nil || r.Resets != nil {
		t.Fatal("missing reset field became zero credits", err)
	}
}

func TestCodexResetSelectionUsesSoonestAvailableCard(t *testing.T) {
	past, soon, later := time.Now().Add(-time.Hour).Unix(), time.Now().Add(time.Hour).Unix(), time.Now().Add(24*time.Hour).Unix()
	r := Reading{Agent: "codex", AccountKey: "id:fixture", Home: t.TempDir(), Resets: &ResetCredits{Count: 3, Credits: []ResetCard{
		{ID: "never", Type: "codexRateLimits", Status: "available"},
		{ID: "late", Type: "codexRateLimits", Status: "available", ExpiresAt: &later},
		{ID: "first", Type: "codexRateLimits", Status: "available", ExpiresAt: &soon},
		{ID: "expired", Type: "codexRateLimits", Status: "available", ExpiresAt: &past},
		{ID: "used", Type: "codexRateLimits", Status: "redeemed", ExpiresAt: &soon},
		{ID: "unknown", Type: "unknown", Status: "available"},
	}}}
	r.Resets.normalize(time.Now())
	a, err := NewResetAttempt(r)
	if err != nil || a.CreditID != "first" || len(a.Key) != 36 || a.AccountKey != r.AccountKey {
		t.Fatalf("wrong selection: %+v %v", a, err)
	}
	r.Resets.Count = 4
	a, err = NewResetAttempt(r)
	if err != nil || a.CreditID != "" {
		t.Fatalf("partial list claims earliest selection: %+v %v", a, err)
	}
	r.AccountKey = ""
	if _, err = NewResetAttempt(r); err == nil {
		t.Fatal("unidentified account can consume a card")
	}
}

func TestCodexResetConsumptionRefreshesAndDoesNotRetry(t *testing.T) {
	for _, outcome := range []string{"reset", "alreadyRedeemed", "noCredit", "nothingToReset"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			log := root + "/requests.jsonl"
			t.Setenv("USAGE_RESET_LOG", log)
			t.Setenv("USAGE_RESET_OUTCOME", outcome)
			a := ResetAttempt{Key: "one-logical-operation", CreditID: "first", AccountKey: "id:fixture", Home: root}
			out, err := useCodexReset(context.Background(), helper(t, "reset-success"), root, a)
			if err != nil || out.Outcome != outcome || len(out.Reading.Windows) != 1 || out.Reading.Windows[0].Used != 0 || out.Reading.Resets.Count != 1 {
				t.Fatalf("wrong outcome or stale quota: %+v %v", out, err)
			}
			data, _ := os.ReadFile(log)
			var params map[string]string
			if json.Unmarshal(data, &params) != nil || !reflect.DeepEqual(params, map[string]string{"idempotencyKey": a.Key, "creditId": a.CreditID}) {
				t.Fatalf("wrong consume payload: %s", data)
			}
		})
	}
}

func TestCodexResetRejectsAccountSwitchAndPreservesAmbiguousAttempt(t *testing.T) {
	root := t.TempDir()
	log := root + "/requests.jsonl"
	t.Setenv("USAGE_RESET_LOG", log)
	a := ResetAttempt{Key: "same-logical-operation", CreditID: "first", AccountKey: "id:fixture", Home: root}
	if _, err := useCodexReset(context.Background(), helper(t, "reset-switched"), root, a); err == nil {
		t.Fatal("account switch was not rejected")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("account switch reached consume")
	}
	for range 2 {
		if _, err := useCodexReset(context.Background(), helper(t, "reset-ambiguous"), root, a); err == nil {
			t.Fatal("ambiguous consume looked successful")
		}
	}
	data, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] != lines[1] {
		t.Fatalf("retry changed idempotency payload: %s", data)
	}
}

func TestCodexResetConfirmedOutcomeSurvivesRefreshFailure(t *testing.T) {
	root := t.TempDir()
	t.Setenv("USAGE_RESET_LOG", root+"/requests.jsonl")
	out, err := useCodexReset(context.Background(), helper(t, "reset-refresh-failed"), root, ResetAttempt{Key: "one-operation", AccountKey: "id:fixture", Home: root})
	if err != nil || out.Outcome != "reset" || out.Reading.Error == "" {
		t.Fatalf("confirmed use became retryable after a read failure: %+v %v", out, err)
	}
}
