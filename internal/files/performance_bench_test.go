package files

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// These fixtures exercise the public bounded file API, with no real project or
// user data. Keep them unchanged between revisions when comparing profiles.
func BenchmarkDirectoryList(b *testing.B) {
	for _, count := range []int{100, 1000, MaxEntries} {
		b.Run(fmt.Sprintf("entries=%d", count), func(b *testing.B) {
			dir := b.TempDir()
			for i := range count {
				name := fmt.Sprintf("%04d-%s.txt", (i*7919)%count, []string{"Alpha", "beta", "中文", "Éclair"}[i%4])
				if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture\n"), 0600); err != nil {
					b.Fatal(err)
				}
			}
			ctx := context.Background()
			req := Request{Kind: "list", Path: dir}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				r, err := Do(ctx, req)
				if err != nil || len(r.Entries) != count || r.Truncated {
					b.Fatalf("list returned %d entries (truncated=%v): %v", len(r.Entries), r.Truncated, err)
				}
			}
		})
	}
}

func BenchmarkFileRangeRead(b *testing.B) {
	path := filepath.Join(b.TempDir(), "preview.txt")
	data := bytes.Repeat([]byte("preview 中\n"), MaxRead/8)
	if err := os.WriteFile(path, data[:MaxRead+4096], 0600); err != nil {
		b.Fatal(err)
	}
	for _, length := range []int{4096, 512 << 10, MaxRead} {
		b.Run(fmt.Sprintf("bytes=%d", length), func(b *testing.B) {
			req := Request{Kind: "read", Path: path, Offset: 4096, Length: length}
			ctx := context.Background()
			b.ReportAllocs()
			b.SetBytes(int64(length))
			for b.Loop() {
				r, err := Do(ctx, req)
				if err != nil || len(r.Data) != length {
					b.Fatalf("read returned %d bytes: %v", len(r.Data), err)
				}
			}
		})
	}
}
