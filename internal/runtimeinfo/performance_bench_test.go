package runtimeinfo

import "testing"

func BenchmarkCurrent(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if info := Current(); info.Go == "" {
			b.Fatal("missing runtime version")
		}
	}
}
