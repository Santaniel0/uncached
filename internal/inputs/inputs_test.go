package inputs

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Santaniel0/uncached/internal/testlog"
)

var (
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
)

func write(t *testing.T, name, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	touch(t, name, mtime)
}

func touch(t *testing.T, name string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(name, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func newObserver(env map[string]string) Observer {
	return Observer{
		LookupEnv: func(k string) (string, bool) {
			v, ok := env[k]
			return v, ok
		},
		ContentLimit: 1 << 20,
		Workers:      2,
	}
}

func observeOne(t *testing.T, o Observer, in testlog.Input) Observation {
	t.Helper()
	obs, err := o.Observe(context.Background(), []testlog.Input{in})
	if err != nil {
		t.Fatal(err)
	}
	return obs[0]
}

func kinds(changes []Change) []ChangeKind {
	var out []ChangeKind
	for _, c := range changes {
		out = append(out, c.Kind)
	}
	return out
}

func TestObserveAndCompareFiles(t *testing.T) {
	dir := t.TempDir()
	golden := filepath.Join(dir, "testdata", "a.golden")
	big := filepath.Join(dir, "testdata", "big.bin")
	listed := filepath.Join(dir, "testdata")
	missing := filepath.Join(dir, "testdata", "later.txt")
	inputs := []testlog.Input{
		{Op: testlog.OpOpen, Name: golden},
		{Op: testlog.OpOpen, Name: big},
		{Op: testlog.OpOpen, Name: missing},
		{Op: testlog.OpGetenv, Name: "REGION"},
		{Op: testlog.OpGetenv, Name: "UNSET"},
	}

	tests := []struct {
		name   string
		mutate func(t *testing.T)
		env    map[string]string
		want   map[testlog.Input][]ChangeKind
	}{
		{
			name:   "nothing changed",
			mutate: func(*testing.T) {},
			want:   map[testlog.Input][]ChangeKind{},
		},
		{
			name:   "checkout-style mtime change",
			mutate: func(t *testing.T) { touch(t, golden, t1) },
			want:   map[testlog.Input][]ChangeKind{inputs[0]: {ChangeMTimeOnly}},
		},
		{
			name:   "same size, new content",
			mutate: func(t *testing.T) { write(t, golden, "HELLO", t1) },
			want:   map[testlog.Input][]ChangeKind{inputs[0]: {ChangeContent}},
		},
		{
			name:   "new size",
			mutate: func(t *testing.T) { write(t, golden, "hello, world", t0) },
			want:   map[testlog.Input][]ChangeKind{inputs[0]: {ChangeContent}},
		},
		{
			name:   "large file is not hashed",
			mutate: func(t *testing.T) { touch(t, big, t1) },
			want:   map[testlog.Input][]ChangeKind{inputs[1]: {ChangeMTime}},
		},
		{
			name:   "file appears and disappears",
			mutate: func(t *testing.T) { write(t, missing, "x", t0); os.Remove(golden) },
			want: map[testlog.Input][]ChangeKind{
				inputs[0]: {ChangeDeleted},
				inputs[2]: {ChangeCreated},
			},
		},
		{
			name:   "env changes",
			mutate: func(*testing.T) {},
			env:    map[string]string{"REGION": "us", "UNSET": ""},
			want: map[testlog.Input][]ChangeKind{
				inputs[3]: {ChangeEnvValue},
				inputs[4]: {ChangeEnvSet},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			write(t, golden, "hello", t0)
			write(t, big, string(make([]byte, 2<<20)), t0)
			os.Remove(missing)
			touch(t, listed, t0)

			before, err := newObserver(map[string]string{"REGION": "eu"}).Observe(context.Background(), inputs)
			if err != nil {
				t.Fatal(err)
			}
			tt.mutate(t)
			env := tt.env
			if env == nil {
				env = map[string]string{"REGION": "eu"}
			}
			after, err := newObserver(env).Observe(context.Background(), inputs)
			if err != nil {
				t.Fatal(err)
			}
			got := map[testlog.Input][]ChangeKind{}
			for i := range inputs {
				if k := kinds(Compare(before[i], after[i])); len(k) > 0 {
					got[inputs[i]] = k
				}
			}
			if len(got) != len(tt.want) {
				t.Fatalf("changes = %v, want %v", got, tt.want)
			}
			for in, k := range tt.want {
				if !slices.Equal(got[in], k) {
					t.Errorf("%v: changes %v, want %v", in, got[in], k)
				}
			}
		})
	}
}

func TestDirectoryListing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "testdata")
	write(t, filepath.Join(dir, "a.txt"), "a", t0)
	write(t, filepath.Join(dir, "b.txt"), "b", t0)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "sub", "x"), "x", t0)
	touch(t, filepath.Join(dir, "sub"), t0)
	touch(t, dir, t0)

	open := testlog.Input{Op: testlog.OpOpen, Name: dir}
	stat := testlog.Input{Op: testlog.OpStat, Name: dir}
	o := newObserver(nil)
	before := observeOne(t, o, open)
	beforeStat := observeOne(t, o, stat)
	if n := len(before.File.Entries); n != 3 {
		t.Fatalf("listing has %d entries, want 3", n)
	}
	if beforeStat.File.Entries != nil {
		t.Error("a stat of a directory should not record a listing")
	}

	// A fresh checkout: same names and contents, new timestamps.
	for _, p := range []string{"a.txt", "b.txt", "sub"} {
		touch(t, filepath.Join(dir, p), t1)
	}
	touch(t, dir, t1)
	changes := Compare(before, observeOne(t, o, open))
	if len(changes) != 1 || changes[0].Kind != ChangeListing || !changes[0].TimestampOnly() {
		t.Fatalf("after re-touch: %+v", changes)
	}
	statChanges := Compare(beforeStat, observeOne(t, o, stat))
	if len(statChanges) != 1 || statChanges[0].Kind != ChangeMTimeOnly {
		t.Fatalf("stat after re-touch: %+v", statChanges)
	}

	// Real edits: one file removed, one added, one rewritten.
	os.Remove(filepath.Join(dir, "a.txt"))
	write(t, filepath.Join(dir, "c.txt"), "c", t1)
	write(t, filepath.Join(dir, "b.txt"), "B", t1)
	changes = Compare(before, observeOne(t, o, open))
	if len(changes) != 1 || changes[0].TimestampOnly() {
		t.Fatalf("after edits: %+v", changes)
	}
	want := []EntryChange{
		{Name: "a.txt", Kind: ChangeDeleted},
		{Name: "b.txt", Kind: ChangeContent},
		{Name: "c.txt", Kind: ChangeCreated},
		{Name: "sub", Kind: ChangeMTimeOnly, IsDir: true},
	}
	if !slices.Equal(changes[0].Entries, want) {
		t.Errorf("entries:\n got %+v\nwant %+v", changes[0].Entries, want)
	}
}

func TestKnownDigestsAreReused(t *testing.T) {
	name := filepath.Join(t.TempDir(), "f")
	write(t, name, "one", t0)
	in := testlog.Input{Op: testlog.OpOpen, Name: name}
	o := newObserver(nil)
	first := observeOne(t, o, in)

	// Pretend the earlier digest was something else. With identical stat
	// data the observer must trust it instead of rereading the file.
	known := *first.File
	known.Digest = "feed"
	o.Known = map[string]FileState{name: known}
	if got := observeOne(t, o, in).File.Digest; got != "feed" {
		t.Errorf("digest = %q, want the known one", got)
	}
	touch(t, name, t1)
	if got := observeOne(t, o, in).File.Digest; got != first.File.Digest {
		t.Errorf("after mtime change digest = %q, want a fresh %q", got, first.File.Digest)
	}
}

func TestKnownFilesFlattensListings(t *testing.T) {
	obs := []Observation{{
		Input: testlog.Input{Op: testlog.OpOpen, Name: filepath.FromSlash("/m/testdata")},
		File: &FileState{Exists: true, IsDir: true, Entries: []DirEntry{
			{Name: "a", Size: 1, Digest: "d1", ModTime: t0},
		}},
	}}
	known := KnownFiles(obs)
	got, ok := known[filepath.FromSlash("/m/testdata/a")]
	if !ok || got.Digest != "d1" || !got.Exists {
		t.Errorf("known = %+v", known)
	}
}

func TestChangeKindText(t *testing.T) {
	for k := ChangeEnvSet; k <= ChangeAccess; k++ {
		b, err := k.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var back ChangeKind
		if err := back.UnmarshalText(b); err != nil || back != k {
			t.Errorf("round trip of %v: %v, %v", k, back, err)
		}
	}
	if _, err := ChangeKind(0).MarshalText(); err == nil {
		t.Error("zero ChangeKind marshaled")
	}
}
