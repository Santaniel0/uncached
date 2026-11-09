package report

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Santaniel0/uncached/internal/explain"
	"github.com/Santaniel0/uncached/internal/fingerprint"
	"github.com/Santaniel0/uncached/internal/gotest"
	"github.com/Santaniel0/uncached/internal/inputs"
	"github.com/Santaniel0/uncached/internal/testlog"
)

var (
	t0   = time.Date(2026, 11, 9, 10, 0, 1, 0, time.UTC)
	t1   = time.Date(2026, 11, 9, 10, 2, 13, 0, time.UTC)
	root = filepath.FromSlash("/m")
)

func sample() explain.Report {
	golden := testlog.Input{Op: testlog.OpOpen, Name: filepath.Join(root, "golden", "testdata", "a.golden")}
	listing := testlog.Input{Op: testlog.OpOpen, Name: filepath.Join(root, "catalog", "testdata")}
	return explain.Report{
		Mode:        explain.ModeRun,
		GeneratedAt: t1,
		Previous:    &explain.Snapshot{SavedAt: t0, Commit: "3f9c2e1"},
		Packages: []explain.Package{
			{Package: "example.com/m/money", Root: root, Outcome: gotest.OutcomeCached},
			{Package: "example.com/m/golden", Root: root, Outcome: gotest.OutcomePassed, Reasons: []explain.Reason{{
				Kind: explain.KindInputsChanged,
				Inputs: []inputs.Change{
					{Kind: inputs.ChangeMTimeOnly, Input: golden, OldTime: t0, NewTime: t1},
					{Kind: inputs.ChangeEnvValue, Input: testlog.Input{Op: testlog.OpGetenv, Name: "REGION"}},
					{Kind: inputs.ChangeListing, Input: listing, IsDir: true, OldTime: t0, NewTime: t1, Entries: []inputs.EntryChange{
						{Name: "new.json", Kind: inputs.ChangeCreated},
						{Name: "old.json", Kind: inputs.ChangeDeleted},
						{Name: "sub", Kind: inputs.ChangeMTimeOnly, IsDir: true},
					}},
				},
			}}},
			{Package: "example.com/m/invoice", Root: root, Outcome: gotest.OutcomeFailed, Reasons: []explain.Reason{
				{Kind: explain.KindBinaryChanged, Binary: []fingerprint.Diff{
					{Kind: fingerprint.DiffPackageChanged, Subject: "example.com/m/money", Files: []fingerprint.FileChange{{Name: "money.go", Kind: fingerprint.FileModified}}},
					{Kind: fingerprint.DiffPackageChanged, Subject: "example.com/m/invoice [example.com/m/invoice.test]"},
					{Kind: fingerprint.DiffModuleChanged, Subject: "golang.org/x/text", Old: "v0.20.0", New: "v0.21.0"},
					{Kind: fingerprint.DiffSettingChanged, Subject: "-race", New: "true"},
				}},
				{Kind: explain.KindTestFailed},
			}},
			{Package: "example.com/m/flags", Root: root, Outcome: gotest.OutcomePassed, Reasons: []explain.Reason{
				{Kind: explain.KindCachingDisabled, Detail: "-count=1"},
				{Kind: explain.KindInputTooNew, Detail: filepath.Join(root, "flags", "out.txt")},
			}},
		},
		Notes: []explain.Note{{Kind: explain.NoteTimestampsOnly, Count: 1}},
	}
}

func TestText(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, sample(), Options{}); err != nil {
		t.Fatal(err)
	}
	want := `uncached: 1 of 4 packages cached; compared with the run of 2026-11-09 10:00:01 UTC (commit 3f9c2e1)

example.com/m/golden
  test inputs changed, test binary unchanged:
    golden/testdata/a.golden: mtime changed, content identical (2026-11-09 10:00:01 UTC -> 2026-11-09 10:02:13 UTC)
    env REGION: value changed
    catalog/testdata/: listing changed: added: new.json; removed: old.json; new mtime, same content: sub/

example.com/m/invoice
  test binary changed:
    example.com/m/money: money.go modified
    example.com/m/invoice: source files changed
    module golang.org/x/text: v0.20.0 -> v0.21.0
    -race: (unset) -> true
  the test failed; failed results are never cached

example.com/m/flags
  caching disabled by the test flag -count=1
  flags/out.txt was modified less than 2 seconds before go test checked it, so the result was not cached

note: 1 package missed the cache only because file mtimes changed while the content stayed
      the same. A fresh git checkout does exactly this. Run ` + "`uncached restore-mtime`" + ` before
      go test so that mtimes match between checkouts.
`
	if got := buf.String(); got != want {
		t.Errorf("text report mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestTextSummary(t *testing.T) {
	var buf bytes.Buffer
	if err := Text(&buf, sample(), Options{Summary: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "example.com/m/golden") {
		t.Errorf("summary lists packages:\n%s", buf.String())
	}
	if !strings.HasPrefix(buf.String(), "uncached: 1 of 4 packages cached") || !strings.Contains(buf.String(), "note: 1 package") {
		t.Errorf("summary:\n%s", buf.String())
	}
}

func TestTextDiffHeaderAndOtherReasons(t *testing.T) {
	rep := explain.Report{
		Mode:     explain.ModeDiff,
		Previous: &explain.Snapshot{SavedAt: t0},
		Current:  &explain.Snapshot{SavedAt: t1, Commit: "abc"},
		Packages: []explain.Package{
			{Package: "a", Reasons: []explain.Reason{{Kind: explain.KindNewPackage}}},
			{Package: "b", Reasons: []explain.Reason{{Kind: explain.KindCacheEntryMissing, Detail: "x"}}},
			{Package: "c", Reasons: []explain.Reason{{Kind: explain.KindInputsChanged, Detail: "no earlier record"}}},
		},
		Notes: []explain.Note{{Kind: explain.NoteNoPreviousState, Detail: "/c/state.json"}},
	}
	var buf bytes.Buffer
	if err := Text(&buf, rep, Options{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"uncached: 0 of 3 test packages unchanged between 2026-11-09 10:00:01 UTC and 2026-11-09 10:02:13 UTC (commit abc)\n",
		"  no earlier record of this test to compare with\n",
		"  nothing changed, but the cached result is gone: x (cache not restored, trimmed, or evicted)\n",
		"  test inputs changed, test binary unchanged: no earlier record\n",
		"note: no earlier state at\n        /c/state.json\n",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %q in:\n%s", want, buf.String())
		}
	}
}

func TestJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, sample()); err != nil {
		t.Fatal(err)
	}
	var back explain.Report
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Packages) != 4 || back.Packages[2].Reasons[0].Binary[2].New != "v0.21.0" {
		t.Errorf("JSON round trip lost data:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `"kind": "mtime-only"`) {
		t.Errorf("enums are not written as names:\n%s", buf.String())
	}
}
