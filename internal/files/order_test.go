package files

import (
	"slices"
	"testing"
)

// Include case-only pairs directly: case-insensitive host filesystems cannot
// create them, but results from a case-sensitive remote host still need the
// same deterministic order, with directories first and original-name ties.
func TestDirectoryOrderingAcrossCaseAndUnicode(t *testing.T) {
	entries := []Entry{{Name: "中文"}, {Name: "banana"}, {Name: "éclair"}, {Name: "zDir", Directory: true},
		{Name: "Éclair"}, {Name: "Banana"}, {Name: "apple"}, {Name: "ADir", Directory: true}}
	want := []string{"ADir", "zDir", "apple", "Banana", "banana", "Éclair", "éclair", "中文"}
	for i := range len(entries) {
		// Reorder the input between runs so keys and entries must stay aligned.
		entries = append(entries[1:], entries[0])
		sortEntries(entries)
		got := make([]string, len(entries))
		for j, e := range entries {
			got[j] = e.Name
		}
		if !slices.Equal(got, want) {
			t.Fatalf("permutation %d: got %q, want %q", i, got, want)
		}
	}
}
