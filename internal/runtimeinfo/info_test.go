package runtimeinfo

import "testing"

func TestVersionInitializationAfterEarlyBuildQuery(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = ""
	early := Current()
	Version = "fixture-release"
	later := Current()
	if later.Version != Version || early.Go == "" || later.Go != early.Go {
		t.Fatalf("entrypoint version was frozen by an early query: %+v / %+v", early, later)
	}
	// Results are values: changing one diagnostic must not affect later callers.
	later.Go, later.MyGo = "modified", "modified"
	if next := Current(); next.Go != early.Go || next.MyGo != early.MyGo {
		t.Fatalf("caller modified cached metadata: %+v", next)
	}
}
