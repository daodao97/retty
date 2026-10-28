package agentusage

import (
	"context"
	"sync"
	"time"
)

// Snapshot is a read-only, credential-free view for paired devices. It contains
// neither configuration paths nor reset-credit IDs or redemption operations.
type Snapshot struct {
	At       time.Time       `json:"at"`
	Readings []PublicReading `json:"readings"`
}
type PublicReading struct {
	Agent                string    `json:"agent"`
	Plan                 string    `json:"plan,omitempty"`
	Account              string    `json:"account,omitempty"`
	Windows              []Window  `json:"windows"`
	At                   time.Time `json:"at"`
	Error                string    `json:"error,omitempty"`
	ResetCount           *int      `json:"resetCount,omitempty"`
	ResetExpiries        []*int64  `json:"resetExpiries,omitempty"`
	ResetDetailsComplete bool      `json:"resetDetailsComplete,omitempty"`
}

func Public(r Reading) PublicReading {
	p := PublicReading{Agent: r.Agent, Plan: r.Plan, Account: r.Account, Windows: r.Windows, At: r.At, Error: r.Error}
	if r.Resets != nil {
		count := r.Resets.Count
		p.ResetCount = &count
		for _, c := range r.Resets.Credits {
			p.ResetExpiries = append(p.ResetExpiries, c.ExpiresAt)
		}
		p.ResetDetailsComplete = r.Resets.Credits != nil && len(r.Resets.Credits) == r.Resets.Count
	}
	return p
}
func CollectSnapshot(ctx context.Context) Snapshot {
	s := Snapshot{Readings: make([]PublicReading, 2)}
	var wg sync.WaitGroup
	for i, agent := range []string{"codex", "claude"} {
		wg.Go(func() { s.Readings[i] = Public(Read(ctx, agent)) })
	}
	wg.Wait()
	s.At = time.Now()
	return s
}
