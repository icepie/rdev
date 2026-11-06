package client

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFileListingPagination(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "z-dir"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b", "a", "c"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		offset int64
		want   []string
	}{
		{0, []string{"z-dir", "a"}}, {2, []string{"b", "c"}},
		{3, []string{"c"}}, {4, []string{}}, {1 << 62, []string{}}, {-1, []string{"z-dir", "a"}},
	} {
		entries, total, err := listFileEntries(dir, tc.offset, 2)
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name)
		}
		if total != 4 || !reflect.DeepEqual(names, tc.want) {
			t.Fatalf("offset %d: total=%d names=%v want=%v", tc.offset, total, names, tc.want)
		}
	}
}
