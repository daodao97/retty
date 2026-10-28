package agentusage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPhoneSnapshotExcludesPrivateAccountAndRedemptionData(t *testing.T) {
	r := Reading{Agent: "codex", Home: "/private/config", AccountKey: "credential-key", Resets: &ResetCredits{Count: 1, Credits: []ResetCard{{ID: "private-redemption-id"}}}}
	p := Public(r)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{r.Home, r.AccountKey, r.Resets.Credits[0].ID} {
		if strings.Contains(string(data), secret) {
			t.Fatal("phone snapshot contains private source metadata")
		}
	}
	if p.ResetCount == nil || *p.ResetCount != 1 || !p.ResetDetailsComplete || len(p.ResetExpiries) != 1 || p.ResetExpiries[0] != nil {
		t.Fatal("unknown expiry/count lost", p)
	}
}
