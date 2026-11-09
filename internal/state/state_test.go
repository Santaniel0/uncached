package state

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Santaniel0/uncached/internal/fingerprint"
	"github.com/Santaniel0/uncached/internal/ids"
	"github.com/Santaniel0/uncached/internal/inputs"
	"github.com/Santaniel0/uncached/internal/testlog"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	when := time.Date(2026, 11, 9, 12, 0, 0, 5, time.UTC)
	id, err := ids.ParseActionID(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	golden := testlog.Input{Op: testlog.OpOpen, Name: filepath.FromSlash("/m/testdata/a.golden")}
	s := New()
	s.SavedAt = when
	s.Commit = "3f9c2e1"
	s.Tests["example.com/m/a"] = &TestRecord{
		Dir:     filepath.FromSlash("/m/a"),
		Root:    filepath.FromSlash("/m"),
		TestIDs: []ids.ActionID{id},
		InputID: id,
		Inputs: []inputs.Observation{
			{Input: testlog.GODEBUG, Env: &inputs.EnvState{Set: true, Digest: "d"}},
			{Input: golden, File: &inputs.FileState{Exists: true, Size: 5, ModTime: when, Digest: "c"}},
		},
		Binary:     fingerprint.Test{Sources: map[ids.ImportPath]string{"example.com/m/a": "x"}},
		Config:     fingerprint.Config{"GOVERSION": "go1.27.3"},
		RecordedAt: when,
	}
	s.Files["example.com/m/a"] = map[string]string{"a.go": "x"}
	if err := Save(path, s); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := got.Tests["example.com/m/a"]
	if rec == nil || !rec.HasTestID([]ids.ActionID{id}) || rec.InputID != id {
		t.Fatalf("record = %+v", rec)
	}
	if !rec.Inputs[1].File.ModTime.Equal(when) || rec.Inputs[1].Input != golden {
		t.Errorf("observation = %+v", rec.Inputs[1])
	}
	if got.Commit != "3f9c2e1" || !got.SavedAt.Equal(when) || got.Files["example.com/m/a"]["a.go"] != "x" {
		t.Errorf("state = %+v", got)
	}
	if ins := got.Inputs(); !slices.Equal(ins, []testlog.Input{testlog.GODEBUG, golden}) {
		t.Errorf("Inputs() = %v", ins)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("state directory holds %d files, want only the state file", len(entries))
	}
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "missing.json")); !errors.Is(err, ErrNoState) {
		t.Errorf("missing file: err = %v", err)
	}
	old := filepath.Join(dir, "old.json")
	os.WriteFile(old, []byte(`{"schema":99,"tests":{}}`), 0o644)
	if _, err := Load(old); !errors.Is(err, ErrSchema) {
		t.Errorf("old schema: err = %v", err)
	}
	broken := filepath.Join(dir, "broken.json")
	os.WriteFile(broken, []byte(`{"schema":1,"tests":`), 0o644)
	if _, err := Load(broken); err == nil || errors.Is(err, ErrNoState) {
		t.Errorf("broken file: err = %v", err)
	}
}

func TestDefaultPath(t *testing.T) {
	a := DefaultPath(filepath.FromSlash("/cache"), filepath.FromSlash("/m/go.mod"), filepath.FromSlash("/m"))
	b := DefaultPath(filepath.FromSlash("/cache"), filepath.FromSlash("/n/go.mod"), filepath.FromSlash("/m"))
	c := DefaultPath(filepath.FromSlash("/cache"), os.DevNull, filepath.FromSlash("/m"))
	if a == b || a == c {
		t.Errorf("paths should differ per module: %s %s %s", a, b, c)
	}
	if filepath.Dir(a) != filepath.Join(filepath.FromSlash("/cache"), "uncached") {
		t.Errorf("state is not under GOCACHE/uncached: %s", a)
	}
	if got := ReportPath(a); !strings.HasSuffix(got, ".report.json") {
		t.Errorf("ReportPath = %s", got)
	}
}
