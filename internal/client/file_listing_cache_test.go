package client

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDirectoryPagingRefreshesAndDoesNotExposeMutableCache(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cache := fileListingCache{}
	first, total, err := cache.list(dir, 0, 2)
	if err != nil || total != 5 || len(first) != 2 {
		t.Fatal(first, total, err)
	}
	first[0].Name = "corrupt"
	next, total, err := cache.list(dir, 2, 2)
	if err != nil || total != 5 || next[0].Name != "2" {
		t.Fatal(next, total, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	cache.invalidate()
	_, total, err = cache.list(dir, 2, 2)
	if err != nil || total != 6 {
		t.Fatal(total, err)
	}
	fresh, _, _ := cache.list(dir, 0, 2)
	if fresh[0].Name != "0" {
		t.Fatal("caller mutated shared cache")
	}
}
func BenchmarkDirectoryPaging(b *testing.B) {
	for _, count := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			dir := b.TempDir()
			for i := 0; i < count; i++ {
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprint(i)), nil, 0600); err != nil {
					b.Fatal(err)
				}
			}
			var cache fileListingCache
			if _, _, err := cache.list(dir, 0, 200); err != nil {
				b.Fatal(err)
			}
			b.Run("uncached", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, _, err := listFileEntries(dir, 200, 200); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("cached", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, _, err := cache.list(dir, 200, 200); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}
