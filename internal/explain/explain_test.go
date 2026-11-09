package explain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Santaniel0/uncached/internal/fingerprint"
	"github.com/Santaniel0/uncached/internal/gotest"
	"github.com/Santaniel0/uncached/internal/ids"
	"github.com/Santaniel0/uncached/internal/inputs"
	"github.com/Santaniel0/uncached/internal/state"
	"github.com/Santaniel0/uncached/internal/testlog"
)

const pkg ids.ImportPath = "example.com/m/a"

var (
	now     = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	t0      = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1      = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	golden  = testlog.Input{Op: testlog.OpOpen, Name: filepath.FromSlash("/m/a/testdata/a.golden")}
	region  = testlog.Input{Op: testlog.OpGetenv, Name: "REGION"}
	testID  = id("test")
	inputID = id("inputs")
)

func id(seed string) ids.ActionID { return ids.ActionID(sha256.Sum256([]byte(seed))) }

func line(format string, args ...any) string { return fmt.Sprintf("testcache: "+format, args...) }

func debugLog(lines ...string) *gotest.DebugLog {
	d := gotest.NewDebugLog()
	for _, l := range lines {
		d.Line(l)
	}
	return d
}

func fileObs(in testlog.Input, mtime time.Time, digest string) inputs.Observation {
	return inputs.Observation{Input: in, File: &inputs.FileState{Exists: true, Size: 5, Mode: 0o644, ModTime: mtime, Digest: digest}}
}

func envObs(in testlog.Input, digest string) inputs.Observation {
	return inputs.Observation{Input: in, Env: &inputs.EnvState{Set: true, Digest: digest}}
}

func prevState(rec *state.TestRecord) *state.State {
	s := state.New()
	s.SavedAt = t0
	s.Commit = "abc1234"
	if rec != nil {
		s.Tests[pkg] = rec
	}
	return s
}

func record() *state.TestRecord {
	return &state.TestRecord{
		Root:    filepath.FromSlash("/m"),
		TestIDs: []ids.ActionID{testID},
		InputID: inputID,
		Inputs:  []inputs.Observation{fileObs(golden, t0, "same"), envObs(region, "eu")},
		Binary:  fingerprint.Test{Sources: map[ids.ImportPath]string{pkg: "v1"}},
		Config:  fingerprint.Config{"GOVERSION": "go1.27.3"},
	}
}

func preRun(obs ...inputs.Observation) map[testlog.Input]inputs.Observation {
	m := map[testlog.Input]inputs.Observation{}
	for _, o := range obs {
		m[o.Input] = o
	}
	return m
}

var (
	lookupLine   = line("%s: test ID 3870 => %s", pkg, testID)
	listMissing  = line("%s: input list not found: cache entry not found", pkg)
	hashedLine   = line("%s: test ID %s => input ID %s => %s", pkg, testID, id("new inputs"), id("key"))
	outputMissed = line("%s: test output not found: cache entry not found", pkg)
)

func TestFromRun(t *testing.T) {
	changedBinary := record()
	changedBinary.Binary = fingerprint.Test{Sources: map[ids.ImportPath]string{pkg: "v2"}}
	failedBefore := record()
	failedBefore.TestIDs = nil
	failedBefore.NoResult = true

	tests := []struct {
		name    string
		outcome gotest.Outcome
		debug   *gotest.DebugLog
		prev    *state.State
		current *state.TestRecord
		preRun  map[testlog.Input]inputs.Observation
		want    []Kind
		check   func(t *testing.T, rs []Reason)
	}{
		{
			name:    "cached",
			outcome: gotest.OutcomeCached,
			debug:   debugLog(lookupLine),
			prev:    prevState(record()),
		},
		{
			name:    "first run",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(lookupLine, listMissing),
			want:    []Kind{KindNewPackage},
		},
		{
			name:    "binary changed",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(lookupLine, listMissing),
			prev:    prevState(&state.TestRecord{TestIDs: []ids.ActionID{id("old")}, Binary: record().Binary, Config: record().Config}),
			current: changedBinary,
			want:    []Kind{KindBinaryChanged},
			check: func(t *testing.T, rs []Reason) {
				if len(rs[0].Binary) != 1 || rs[0].Binary[0].Kind != fingerprint.DiffPackageChanged {
					t.Errorf("binary diffs = %+v", rs[0].Binary)
				}
			},
		},
		{
			name:    "binary unchanged but entry gone",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(lookupLine, listMissing),
			prev:    prevState(record()),
			current: record(),
			want:    []Kind{KindCacheEntryMissing},
		},
		{
			name:    "flaky test failed last time",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(lookupLine, listMissing),
			prev:    prevState(failedBefore),
			current: record(),
			want:    []Kind{KindNoEarlierResult},
		},
		{
			name:    "mtime-only input change",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(lookupLine, hashedLine, outputMissed),
			prev:    prevState(record()),
			preRun:  preRun(fileObs(golden, t1, "same"), envObs(region, "eu")),
			want:    []Kind{KindInputsChanged},
			check: func(t *testing.T, rs []Reason) {
				if len(rs[0].Inputs) != 1 || rs[0].Inputs[0].Kind != inputs.ChangeMTimeOnly {
					t.Errorf("input changes = %+v", rs[0].Inputs)
				}
			},
		},
		{
			name:    "env change",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(lookupLine, hashedLine, outputMissed),
			prev:    prevState(record()),
			preRun:  preRun(fileObs(golden, t0, "same"), envObs(region, "us")),
			want:    []Kind{KindInputsChanged},
			check: func(t *testing.T, rs []Reason) {
				if len(rs[0].Inputs) != 1 || rs[0].Inputs[0].Kind != inputs.ChangeEnvValue {
					t.Errorf("input changes = %+v", rs[0].Inputs)
				}
			},
		},
		{
			name:    "inputs unchanged and same input ID",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(lookupLine, line("%s: test ID %s => input ID %s => %s", pkg, testID, inputID, id("key")), outputMissed),
			prev:    prevState(record()),
			preRun:  preRun(fileObs(golden, t0, "same"), envObs(region, "eu")),
			want:    []Kind{KindCacheEntryMissing},
		},
		{
			name:    "inputs hashed differently with nothing to show",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(lookupLine, hashedLine, outputMissed),
			prev:    prevState(record()),
			preRun:  preRun(fileObs(golden, t0, "same"), envObs(region, "eu")),
			want:    []Kind{KindInputsChanged},
			check: func(t *testing.T, rs []Reason) {
				if rs[0].Detail == "" || len(rs[0].Inputs) != 0 {
					t.Errorf("reason = %+v", rs[0])
				}
			},
		},
		{
			name:    "count flag",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(line("caching disabled for test argument: -test.count=1")),
			prev:    prevState(record()),
			want:    []Kind{KindCachingDisabled},
			check: func(t *testing.T, rs []Reason) {
				if rs[0].Detail != "-count=1" {
					t.Errorf("detail = %q", rs[0].Detail)
				}
			},
		},
		{
			name:    "local directory mode",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(line("caching disabled in local directory mode")),
			want:    []Kind{KindLocalDirectoryMode},
		},
		{
			name:    "outside module",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(line("caching disabled for package outside of module root, GOPATH, or GOROOT: %s", pkg)),
			want:    []Kind{KindOutsideModule},
		},
		{
			name:    "test failed after a binary change",
			outcome: gotest.OutcomeFailed,
			debug:   debugLog(lookupLine, listMissing),
			prev:    prevState(&state.TestRecord{TestIDs: []ids.ActionID{id("old")}, Binary: record().Binary}),
			current: changedBinary,
			want:    []Kind{KindBinaryChanged, KindTestFailed},
		},
		{
			name:    "input too new",
			outcome: gotest.OutcomePassed,
			debug: debugLog(lookupLine,
				line("%s: input file %s: file used as input is too new", pkg, golden.Name),
				line("%s: input file %s: file used as input is too new", pkg, golden.Name)),
			prev: prevState(record()),
			want: []Kind{KindInputTooNew},
			check: func(t *testing.T, rs []Reason) {
				if rs[0].Detail != golden.Name {
					t.Errorf("detail = %q", rs[0].Detail)
				}
			},
		},
		{
			name:    "go clean -testcache",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(lookupLine, line("%s: test ID %s => input ID %s => %s", pkg, testID, inputID, id("key")), line("%s: test output expired due to go clean -testcache", pkg)),
			prev:    prevState(record()),
			want:    []Kind{KindTestCacheCleaned},
		},
		{
			name:    "build failed",
			outcome: gotest.OutcomeBuildFailed,
			debug:   debugLog(),
			want:    []Kind{KindBuildFailed},
		},
		{
			name:    "nothing printed",
			outcome: gotest.OutcomePassed,
			debug:   debugLog(),
			want:    []Kind{KindUnknown},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := Run{
				Results: []gotest.Result{{Package: pkg, Outcome: tt.outcome}},
				Debug:   tt.debug,
				Prev:    tt.prev,
				Current: map[ids.ImportPath]*state.TestRecord{},
				PreRun:  tt.preRun,
				Now:     now,
			}
			if tt.current != nil {
				run.Current[pkg] = tt.current
			}
			rep := FromRun(run)
			if len(rep.Packages) != 1 {
				t.Fatalf("report has %d packages", len(rep.Packages))
			}
			var got []Kind
			for _, r := range rep.Packages[0].Reasons {
				got = append(got, r.Kind)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("reasons = %v, want %v (%+v)", got, tt.want, rep.Packages[0].Reasons)
			}
			if tt.check != nil {
				tt.check(t, rep.Packages[0].Reasons)
			}
		})
	}
}

func TestFromRunNotes(t *testing.T) {
	rep := FromRun(Run{
		Results: []gotest.Result{
			{Package: pkg, Outcome: gotest.OutcomePassed},
			{Package: "example.com/m/empty", Outcome: gotest.OutcomeNoTestFiles},
		},
		Debug:  debugLog(lookupLine, hashedLine, outputMissed, line("%s: brand new message", pkg)),
		Prev:   prevState(record()),
		PreRun: preRun(fileObs(golden, t1, "same"), envObs(region, "eu")),
		Now:    now,
	})
	if hits, total := rep.Counts(); hits != 0 || total != 1 {
		t.Errorf("counts = %d/%d, want 0/1 (packages without tests are not counted)", hits, total)
	}
	var kinds []NoteKind
	for _, n := range rep.Notes {
		kinds = append(kinds, n.Kind)
	}
	if !slices.Equal(kinds, []NoteKind{NoteUnrecognizedOutput, NoteTimestampsOnly}) {
		t.Errorf("notes = %+v", rep.Notes)
	}
	if rep.Previous == nil || rep.Previous.Commit != "abc1234" {
		t.Errorf("previous = %+v", rep.Previous)
	}

	empty := FromRun(Run{Debug: debugLog(), Now: now, StatePath: "/cache/uncached/state.json"})
	if len(empty.Notes) != 1 || empty.Notes[0].Kind != NoteNoPreviousState {
		t.Errorf("notes without previous state = %+v", empty.Notes)
	}
}

func TestFromStates(t *testing.T) {
	old := prevState(record())
	old.Tests["example.com/m/same"] = record()
	old.Tests["example.com/m/bin"] = record()

	cur := state.New()
	cur.SavedAt = t1
	touched := record()
	touched.InputID = id("other")
	touched.Inputs = []inputs.Observation{fileObs(golden, t1, "same"), envObs(region, "eu")}
	cur.Tests[pkg] = touched
	cur.Tests["example.com/m/same"] = record()
	rebuilt := record()
	rebuilt.TestIDs = []ids.ActionID{id("new binary")}
	rebuilt.Config = fingerprint.Config{"GOVERSION": "go1.27.4"}
	cur.Tests["example.com/m/bin"] = rebuilt
	cur.Tests["example.com/m/new"] = record()

	rep := FromStates(old, cur, now)
	got := map[ids.ImportPath][]Kind{}
	for _, p := range rep.Packages {
		for _, r := range p.Reasons {
			got[p.Package] = append(got[p.Package], r.Kind)
		}
	}
	want := map[ids.ImportPath][]Kind{
		pkg:                  {KindInputsChanged},
		"example.com/m/bin":  {KindBinaryChanged},
		"example.com/m/new":  {KindNewPackage},
		"example.com/m/same": nil,
	}
	for p, k := range want {
		if !slices.Equal(got[p], k) {
			t.Errorf("%s: reasons %v, want %v", p, got[p], k)
		}
	}
	if hits, total := rep.Counts(); hits != 1 || total != 4 {
		t.Errorf("counts = %d/%d", hits, total)
	}
	if len(rep.Notes) != 1 || rep.Notes[0].Kind != NoteTimestampsOnly {
		t.Errorf("notes = %+v", rep.Notes)
	}
}

func TestReportJSONRoundTrip(t *testing.T) {
	rep := FromRun(Run{
		Results: []gotest.Result{{Package: pkg, Outcome: gotest.OutcomePassed}},
		Debug:   debugLog(lookupLine, hashedLine, outputMissed),
		Prev:    prevState(record()),
		PreRun:  preRun(fileObs(golden, t1, "same"), envObs(region, "us")),
		Now:     now,
	})
	b, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Mode != ModeRun || len(back.Packages) != 1 || back.Packages[0].Reasons[0].Kind != KindInputsChanged ||
		len(back.Packages[0].Reasons[0].Inputs) != 2 {
		t.Errorf("round trip lost data:\n%s", b)
	}
}
