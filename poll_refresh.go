package main

import (
	"slices"

	"retty/internal/rex"
)

// Control polling still runs on schedule, including reconnect and lease
// maintenance. Unchanged snapshots need no new UI frame.
func sessionSnapshotsEqual(a, b map[string]rex.SessionInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for id, in := range a {
		other, ok := b[id]
		if !ok || !sessionInfoEqual(in, other) {
			return false
		}
	}
	return true
}

func sessionInfoEqual(a, b rex.SessionInfo) bool {
	if !a.Agent.Equal(b.Agent) || !slices.Equal(a.Args, b.Args) || !slices.EqualFunc(a.ProgramStatuses, b.ProgramStatuses, func(a, b rex.ProgramStatus) bool {
		if (a.Progress == nil) != (b.Progress == nil) || a.Progress != nil && *a.Progress != *b.Progress {
			return false
		}
		a.Progress, b.Progress = nil, nil
		return a == b
	}) {
		return false
	}
	return a.ID == b.ID && a.PID == b.PID && a.HookExecutable == b.HookExecutable && a.Shell == b.Shell && a.Created.Equal(b.Created) &&
		a.Program == b.Program && a.Idle == b.Idle && a.Dir == b.Dir && a.Title == b.Title &&
		a.LastOutput.Equal(b.LastOutput) && a.LastInput.Equal(b.LastInput) && a.Output == b.Output && a.Bells == b.Bells &&
		a.Exited == b.Exited && a.ExitCode == b.ExitCode && a.Attached == b.Attached && a.Cols == b.Cols && a.Rows == b.Rows &&
		a.SizeLock == b.SizeLock && a.SizeLockDevice == b.SizeLockDevice && a.Resumed == b.Resumed && a.AttentionSeen == b.AttentionSeen
}

func (a *App) applyPolled(host *desktopHost, infos map[string]rex.SessionInfo) bool {
	if a.pollSnapshots == nil {
		a.pollSnapshots = map[*desktopHost]map[string]rex.SessionInfo{}
	}
	old, seen := a.pollSnapshots[host]
	refresh := !seen || !sessionSnapshotsEqual(old, infos)
	a.postMu.Lock()
	refresh = refresh || len(a.posted) > 0
	a.postMu.Unlock()
	// These states have time-based transitions even if the control snapshot
	// is identical: a dropped viewer, delayed unlock or missing session.
	for p := range a.hostPanes(host) {
		in, ok := infos[p.terminal().SID]
		if !ok || p.terminal().streamEnded || p.terminal().remoteAttaching || p.terminal().unlocking || p.terminal().locked != (in.SizeLock != "") {
			refresh = true
		}
	}
	a.pollSnapshots[host] = infos
	a.applyHost(host, infos)
	return refresh
}
