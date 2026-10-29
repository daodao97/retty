package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"retty/internal/automation"
	"retty/internal/rex"
)

func TestAutomationsExtensionReadsHostHistoryWithoutDaemonOrWrites(t *testing.T) {
	client, bridge, socket, _ := fileUploadFixture(t)
	bridge.automationDir = t.TempDir()
	// A disconnected session daemon must not prevent viewing the schedule.
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	item, err := automation.New(automation.Automation{Goal: "检查构建", Dir: t.TempDir(), Every: "1h", Agent: []string{"codex"}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := automation.Save(bridge.automationDir, item); err != nil {
		t.Fatal(err)
	}
	for i := range 55 {
		at := time.Now().Add(time.Duration(i-55) * time.Hour)
		if err := automation.FinishRun(automation.RunPath(bridge.automationDir, item.ID), func(r *automation.Run) {
			*r = automation.Run{Started: at, Finished: at.Add(time.Minute), Agent: "agent", Runner: "runner", State: "done", Rounds: i + 1, Summary: "所有检查通过"}
		}); err != nil {
			t.Fatal(err)
		}
	}
	before := automationFiles(t, bridge.automationDir)
	list, err := ReadAutomations(t.Context(), client, "")
	if err != nil || len(list.Entries) != 1 || len(list.History) != 0 || list.Entries[0].Run.Agent != "agent" {
		t.Fatal("missing host list or wrong session routing", list, err)
	}
	detail, err := ReadAutomations(t.Context(), client, item.ID)
	if err != nil || len(detail.History) != 50 || detail.History[0].Rounds != 55 || detail.History[49].Rounds != 6 || detail.History[0].Summary != "所有检查通过" {
		t.Fatal("history lost results or newest-first order", detail, err)
	}
	if _, err = ReadAutomations(t.Context(), client, "../../other"); err == nil {
		t.Fatal("unknown task accepted")
	}
	after := automationFiles(t, bridge.automationDir)
	if len(before) != len(after) {
		t.Fatal("viewing created files")
	}
	for path, data := range before {
		if after[path] != data {
			t.Fatal("viewing changed a schedule or run", path)
		}
	}
}

func automationFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	result := map[string]string{}
	if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		result[path] = string(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestAutomationsExtensionEmptyAndOversizedRecords(t *testing.T) {
	client, bridge, _, _ := fileUploadFixture(t)
	bridge.automationDir = t.TempDir()
	value, err := ReadAutomations(t.Context(), client, "")
	if err != nil || value.At.IsZero() || len(value.Entries) != 0 {
		t.Fatal("empty list failed", value, err)
	}
	for range 85 {
		item, err := automation.New(automation.Automation{Goal: "fixture", Dir: bridge.automationDir, Every: "1h", Agent: []string{"codex"}}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := automation.Save(bridge.automationDir, item); err != nil {
			t.Fatal(err)
		}
		if err := automation.UpdateRun(automation.RunPath(bridge.automationDir, item.ID), func(r *automation.Run) {
			r.Summary = strings.Repeat("x", 50<<10)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReadAutomations(t.Context(), client, ""); err == nil || !strings.Contains(err.Error(), "过大") {
		t.Fatal("oversized payload was not rejected explicitly", err)
	}
}

func TestAutomationsExtensionLegacyAndCancellation(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		t.Run(map[bool]string{true: "legacy", false: "cancel"}[legacy], func(t *testing.T) {
			control, peer := net.Pipe()
			defer peer.Close()
			client := rex.NewClient(control, func(context.Context) (net.Conn, error) {
				local, server := net.Pipe()
				go func() {
					defer server.Close()
					var req automationRequest
					json.NewDecoder(server).Decode(&req)
					if legacy {
						json.NewEncoder(server).Encode(rex.Response{Error: `unknown op "automations-read"`})
					} else {
						var extra [1]byte
						server.Read(extra[:])
					}
				}()
				return local, nil
			})
			defer client.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()
			_, err := ReadAutomations(ctx, client, "")
			if legacy && !errors.Is(err, ErrAutomationsUnsupported) || !legacy && err == nil {
				t.Fatal("missing legacy/cancellation error", err)
			}
			select {
			case <-client.Closed():
				t.Fatal("view request closed the shared control connection")
			default:
			}
		})
	}
}
