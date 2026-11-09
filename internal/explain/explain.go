// Package explain decides why each test package did or did not come from
// the go test cache, by combining what cmd/go printed with what uncached
// recorded on the previous run.
package explain

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/Santaniel0/uncached/internal/fingerprint"
	"github.com/Santaniel0/uncached/internal/gotest"
	"github.com/Santaniel0/uncached/internal/ids"
	"github.com/Santaniel0/uncached/internal/inputs"
	"github.com/Santaniel0/uncached/internal/state"
	"github.com/Santaniel0/uncached/internal/testlog"
)

// Reason is one explanation for a cache miss.
type Reason struct {
	Kind   Kind               `json:"kind"`
	Detail string             `json:"detail,omitempty"`
	Inputs []inputs.Change    `json:"inputs,omitempty"`
	Binary []fingerprint.Diff `json:"binary,omitempty"`
}

// Package is the verdict for one test package. A package with no reasons
// came from the cache (or, when comparing states, did not change).
type Package struct {
	Package ids.ImportPath `json:"package"`
	// Root is the module root, used to print paths relative to it.
	Root    string         `json:"root,omitempty"`
	Outcome gotest.Outcome `json:"outcome"`
	Reasons []Reason       `json:"reasons,omitempty"`
}

// Hit reports whether the package needed no explanation.
func (p Package) Hit() bool { return len(p.Reasons) == 0 }

// timestampsOnly reports whether the package missed only because of
// mtime changes on inputs whose content is unchanged.
func (p Package) timestampsOnly() bool {
	if len(p.Reasons) != 1 || p.Reasons[0].Kind != KindInputsChanged || len(p.Reasons[0].Inputs) == 0 {
		return false
	}
	for _, c := range p.Reasons[0].Inputs {
		if !c.TimestampOnly() {
			return false
		}
	}
	return true
}

// Snapshot identifies a saved state.
type Snapshot struct {
	SavedAt time.Time `json:"saved_at"`
	Commit  string    `json:"commit,omitempty"`
}

// Note is a run-level remark.
type Note struct {
	Kind   NoteKind `json:"kind"`
	Count  int      `json:"count,omitempty"`
	Detail string   `json:"detail,omitempty"`
}

// Report is the full explanation of a run or of a comparison of two states.
type Report struct {
	Mode        Mode      `json:"mode"`
	GeneratedAt time.Time `json:"generated_at"`
	Previous    *Snapshot `json:"previous,omitempty"`
	Current     *Snapshot `json:"current,omitempty"`
	Packages    []Package `json:"packages"`
	Notes       []Note    `json:"notes,omitempty"`
}

// Counts returns how many packages were hits and how many were considered.
func (r Report) Counts() (hits, total int) {
	for _, p := range r.Packages {
		if p.Hit() {
			hits++
		}
	}
	return hits, len(r.Packages)
}

// AddNote appends a note.
func (r *Report) AddNote(n Note) { r.Notes = append(r.Notes, n) }

// Run is everything known about one go test invocation.
type Run struct {
	Results []gotest.Result
	Debug   *gotest.DebugLog
	// Prev is the state saved by the previous run, or nil.
	Prev *state.State
	// Current holds this run's fingerprints (binary and config) by package.
	Current      map[ids.ImportPath]*state.TestRecord
	CurrentFiles fingerprint.Files
	// PreRun holds the inputs recorded in Prev as they were just before go
	// test started, which is what cmd/go compared against.
	PreRun    map[testlog.Input]inputs.Observation
	Now       time.Time
	StatePath string
}

// FromRun explains a go test run.
func FromRun(r Run) Report {
	rep := Report{Mode: ModeRun, GeneratedAt: r.Now}
	if r.Prev != nil {
		rep.Previous = &Snapshot{SavedAt: r.Prev.SavedAt, Commit: r.Prev.Commit}
	}
	for _, res := range r.Results {
		if !res.Outcome.Ran() && res.Outcome != gotest.OutcomeBuildFailed {
			continue
		}
		p := Package{Package: res.Package, Outcome: res.Outcome}
		prev := r.prevRecord(res.Package)
		switch cur := r.Current[res.Package]; {
		case cur != nil:
			p.Root = cur.Root
		case prev != nil:
			p.Root = prev.Root
		}
		if res.Outcome != gotest.OutcomeCached {
			p.Reasons = r.missReasons(res, prev)
		}
		rep.Packages = append(rep.Packages, p)
	}
	if r.Prev == nil {
		rep.AddNote(Note{Kind: NoteNoPreviousState, Detail: r.StatePath})
	}
	if n := len(r.Debug.Unrecognized); n > 0 {
		rep.AddNote(Note{Kind: NoteUnrecognizedOutput, Count: n, Detail: r.Debug.Unrecognized[0]})
	}
	addTimestampNote(&rep)
	return rep
}

func addTimestampNote(rep *Report) {
	n := 0
	for _, p := range rep.Packages {
		if p.timestampsOnly() {
			n++
		}
	}
	if n > 0 {
		rep.AddNote(Note{Kind: NoteTimestampsOnly, Count: n})
	}
}

func (r Run) prevRecord(p ids.ImportPath) *state.TestRecord {
	if r.Prev == nil {
		return nil
	}
	return r.Prev.Tests[p]
}

func (r Run) missReasons(res gotest.Result, prev *state.TestRecord) []Reason {
	if res.Outcome == gotest.OutcomeBuildFailed {
		return []Reason{{Kind: KindBuildFailed}}
	}
	evs := r.Debug.Events(res.Package)
	var reasons []Reason
	switch {
	case slices.ContainsFunc(evs, func(e gotest.Event) bool { return e.Kind == gotest.EventOutsideModule }):
		reasons = append(reasons, Reason{Kind: KindOutsideModule})
	case r.Debug.LocalDirMode:
		reasons = append(reasons, Reason{Kind: KindLocalDirectoryMode})
	case len(r.Debug.DisabledBy) > 0:
		for _, arg := range r.Debug.DisabledBy {
			reasons = append(reasons, Reason{Kind: KindCachingDisabled, Detail: displayFlag(arg)})
		}
	default:
		reasons = r.lookupReasons(res.Package, evs, prev)
	}
	seen := map[string]bool{}
	for _, e := range evs {
		if e.TooNew() && !seen[e.Path] {
			seen[e.Path] = true
			reasons = append(reasons, Reason{Kind: KindInputTooNew, Detail: e.Path})
		}
	}
	if res.Outcome == gotest.OutcomeFailed {
		reasons = append(reasons, Reason{Kind: KindTestFailed})
	}
	if len(reasons) == 0 {
		reasons = append(reasons, Reason{Kind: KindUnknown})
	}
	return reasons
}

// displayFlag turns cmd/go's "-test.count=1" back into what the user typed.
func displayFlag(arg string) string {
	return strings.Replace(arg, "-test.", "-", 1)
}

func (r Run) lookupReasons(p ids.ImportPath, evs []gotest.Event, prev *state.TestRecord) []Reason {
	var (
		lookups  []ids.ActionID
		hashed   []gotest.Event
		has      = map[gotest.EventKind]bool{}
		coverage string
	)
	for _, e := range evs {
		has[e.Kind] = true
		switch e.Kind {
		case gotest.EventLookup:
			lookups = append(lookups, e.TestID)
		case gotest.EventInputsHashed:
			hashed = append(hashed, e)
		case gotest.EventCoverageMiss:
			coverage = e.Detail
		}
	}
	switch {
	case has[gotest.EventOutputExpired]:
		return []Reason{{Kind: KindTestCacheCleaned}}
	case len(hashed) > 0 && has[gotest.EventOutputMissing]:
		return []Reason{r.inputsReason(prev, hashed[len(hashed)-1].InputID)}
	case len(hashed) > 0 && coverage != "":
		return []Reason{{Kind: KindCoverage, Detail: coverage}}
	case has[gotest.EventOutputMalformed] || has[gotest.EventInputListMalformed]:
		return []Reason{{Kind: KindCacheCorrupt}}
	case has[gotest.EventInputListMissing]:
		return []Reason{r.binaryReason(p, prev, lookups)}
	}
	return nil
}

// inputsReason explains a miss where cmd/go found the input list for the
// test binary but no result for the current input values.
func (r Run) inputsReason(prev *state.TestRecord, inputID ids.ActionID) Reason {
	if prev == nil || prev.Inputs == nil {
		return Reason{Kind: KindInputsChanged, Detail: "no earlier record of this test's inputs to compare with"}
	}
	if changes := inputs.CompareAll(prev.Inputs, r.PreRun); len(changes) > 0 {
		return Reason{Kind: KindInputsChanged, Inputs: changes}
	}
	if !prev.InputID.IsZero() && prev.InputID == inputID {
		return Reason{Kind: KindCacheEntryMissing, Detail: "the result for these exact inputs is no longer in GOCACHE"}
	}
	return Reason{Kind: KindInputsChanged, Detail: "go hashed the inputs differently, but none of the recorded inputs changed; the input list may have changed since it was recorded"}
}

// binaryReason explains a miss where cmd/go had no input list for the test
// ID, meaning it never stored a result for this binary and arguments.
func (r Run) binaryReason(p ids.ImportPath, prev *state.TestRecord, lookups []ids.ActionID) Reason {
	if prev == nil {
		return Reason{Kind: KindNewPackage}
	}
	if prev.HasTestID(lookups) {
		return Reason{Kind: KindCacheEntryMissing, Detail: "the test binary is unchanged, but GOCACHE has no record of it"}
	}
	cur := r.Current[p]
	if cur == nil {
		return Reason{Kind: KindBinaryChanged, Detail: "the current fingerprint could not be computed"}
	}
	diffs := fingerprint.Compare(prev.Side(r.Prev.Files), cur.Side(r.CurrentFiles))
	if len(diffs) == 0 && prev.Failed {
		return Reason{Kind: KindPreviousRunFailed}
	}
	reason := Reason{Kind: KindBinaryChanged, Binary: diffs}
	if len(diffs) == 0 {
		reason.Detail = "no change found in local sources, module versions, go env or flags"
	}
	return reason
}

// FromStates compares two saved states, for example from two CI runs, and
// explains what changed for each test in cur.
func FromStates(old, cur *state.State, now time.Time) Report {
	rep := Report{
		Mode:        ModeDiff,
		GeneratedAt: now,
		Previous:    &Snapshot{SavedAt: old.SavedAt, Commit: old.Commit},
		Current:     &Snapshot{SavedAt: cur.SavedAt, Commit: cur.Commit},
	}
	for _, p := range slices.Sorted(maps.Keys(cur.Tests)) {
		c := cur.Tests[p]
		o := old.Tests[p]
		pkg := Package{Package: p, Root: c.Root}
		switch {
		case o == nil:
			pkg.Reasons = []Reason{{Kind: KindNewPackage}}
		case o.HasTestID(c.TestIDs):
			if o.InputID != c.InputID {
				pkg.Reasons = []Reason{compareRecordedInputs(o, c)}
			}
		default:
			diffs := fingerprint.Compare(o.Side(old.Files), c.Side(cur.Files))
			reason := Reason{Kind: KindBinaryChanged, Binary: diffs}
			if len(diffs) == 0 {
				reason.Detail = "no change found in local sources, module versions, go env or flags"
			}
			pkg.Reasons = []Reason{reason}
		}
		rep.Packages = append(rep.Packages, pkg)
	}
	addTimestampNote(&rep)
	return rep
}

func compareRecordedInputs(o, c *state.TestRecord) Reason {
	cur := make(map[testlog.Input]inputs.Observation, len(c.Inputs))
	for _, obs := range c.Inputs {
		cur[obs.Input] = obs
	}
	if changes := inputs.CompareAll(o.Inputs, cur); len(changes) > 0 {
		return Reason{Kind: KindInputsChanged, Inputs: changes}
	}
	return Reason{Kind: KindInputsChanged, Detail: "the input hash differs, but no recorded input changed"}
}
