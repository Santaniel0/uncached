package gocache

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Santaniel0/uncached/internal/ids"
)

// put writes an entry the way cmd/go's DiskCache.putIndexEntry does.
func put(t *testing.T, root string, id ids.ActionID, data []byte, when time.Time) {
	t.Helper()
	out := sha256.Sum256(data)
	d := &Dir{root: root}
	entry := fmt.Sprintf("v1 %x %x %20d %20d\n", id[:], out[:], len(data), when.UnixNano())
	writeFile(t, d.file(id, "a"), entry)
	writeFile(t, d.file(out, "d"), string(data))
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func actionID(seed string) ids.ActionID {
	return ids.ActionID(sha256.Sum256([]byte(seed)))
}

func TestReadRoundTrip(t *testing.T) {
	root := t.TempDir()
	id := actionID("test")
	when := time.Date(2026, 11, 9, 10, 0, 0, 123, time.UTC)
	log := []byte("# test log\nopen testdata/in.txt\ngetenv HOME\n")
	put(t, root, id, log, when)

	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	data, e, err := d.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(log) {
		t.Errorf("data = %q, want %q", data, log)
	}
	if !e.Time.Equal(when) {
		t.Errorf("time = %v, want %v", e.Time, when)
	}
	if e.Size != int64(len(log)) {
		t.Errorf("size = %d, want %d", e.Size, len(log))
	}
}

func TestReadErrors(t *testing.T) {
	id := actionID("x")
	other := actionID("y")
	valid := fmt.Sprintf("v1 %x %x %20d %20d\n", id[:], other[:], 5, 1)
	tests := []struct {
		name  string
		setup func(t *testing.T, root string)
		want  error
	}{
		{
			name:  "missing entry",
			setup: func(*testing.T, string) {},
			want:  ErrNotFound,
		},
		{
			name: "missing output",
			setup: func(t *testing.T, root string) {
				writeFile(t, (&Dir{root: root}).file(id, "a"), valid)
			},
			want: ErrNotFound,
		},
		{
			name: "short entry",
			setup: func(t *testing.T, root string) {
				writeFile(t, (&Dir{root: root}).file(id, "a"), "v1 abc\n")
			},
			want: ErrCorrupt,
		},
		{
			name: "long entry",
			setup: func(t *testing.T, root string) {
				writeFile(t, (&Dir{root: root}).file(id, "a"), valid+"x")
			},
			want: ErrCorrupt,
		},
		{
			name: "entry for a different ID",
			setup: func(t *testing.T, root string) {
				e := fmt.Sprintf("v1 %x %x %20d %20d\n", other[:], other[:], 5, 1)
				writeFile(t, (&Dir{root: root}).file(id, "a"), e)
			},
			want: ErrCorrupt,
		},
		{
			name: "size mismatch",
			setup: func(t *testing.T, root string) {
				d := &Dir{root: root}
				writeFile(t, d.file(id, "a"), valid)
				writeFile(t, d.file(other, "d"), "too long for five")
			},
			want: ErrCorrupt,
		},
		{
			name: "bad size field",
			setup: func(t *testing.T, root string) {
				e := fmt.Sprintf("v1 %x %x %20s %20d\n", id[:], other[:], "five", 1)
				writeFile(t, (&Dir{root: root}).file(id, "a"), e)
			},
			want: ErrCorrupt,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			tt.setup(t, root)
			d, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = d.Read(id)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Read error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestReadEmptyOutput(t *testing.T) {
	root := t.TempDir()
	id := actionID("empty")
	entry := fmt.Sprintf("v1 %x %x %20d %20d\n", id[:], id[:], 0, 1)
	writeFile(t, (&Dir{root: root}).file(id, "a"), entry)
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	data, _, err := d.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 0 {
		t.Errorf("data = %q, want empty", data)
	}
}

func TestOpenRejectsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	writeFile(t, f, "x")
	if _, err := Open(f); err == nil {
		t.Fatal("Open on a regular file succeeded")
	}
}
