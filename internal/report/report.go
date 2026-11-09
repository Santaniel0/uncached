// Package report renders explain reports as text or JSON.
package report

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/Santaniel0/uncached/internal/explain"
	"github.com/Santaniel0/uncached/internal/fingerprint"
	"github.com/Santaniel0/uncached/internal/ids"
	"github.com/Santaniel0/uncached/internal/inputs"
	"github.com/Santaniel0/uncached/internal/testlog"
)

// JSON writes the report as indented JSON.
func JSON(w io.Writer, r explain.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return nil
}

// Options control the text rendering.
type Options struct {
	// Summary prints only the header line and notes.
	Summary bool
}

// Text writes the report for people. Only packages that need an
// explanation are listed.
func Text(w io.Writer, r explain.Report, opts Options) error {
	bw := bufio.NewWriter(w)
	header(bw, r)
	if !opts.Summary {
		for _, p := range r.Packages {
			if p.Hit() {
				continue
			}
			fmt.Fprintf(bw, "\n%s\n", p.Package)
			for _, reason := range p.Reasons {
				writeReason(bw, p.Root, reason)
			}
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintln(bw)
		writeNote(bw, n)
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

func header(w io.Writer, r explain.Report) {
	hits, total := r.Counts()
	if r.Mode == explain.ModeDiff {
		fmt.Fprintf(w, "uncached: %d of %d test packages unchanged between %s and %s\n",
			hits, total, snapshot(r.Previous), snapshot(r.Current))
		return
	}
	fmt.Fprintf(w, "uncached: %d of %d packages cached", hits, total)
	if r.Previous != nil {
		fmt.Fprintf(w, "; compared with the run of %s", snapshot(r.Previous))
	}
	fmt.Fprintln(w)
}

func snapshot(s *explain.Snapshot) string {
	if s == nil {
		return "an unknown run"
	}
	out := formatTime(s.SavedAt)
	if s.Commit != "" {
		out += " (commit " + s.Commit + ")"
	}
	return out
}

func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func writeReason(w io.Writer, root string, r explain.Reason) {
	const indent = "  "
	switch r.Kind {
	case explain.KindBinaryChanged:
		fmt.Fprintf(w, "%stest binary changed%s\n", indent, suffix(r.Detail, len(r.Binary) > 0))
		for _, d := range r.Binary {
			fmt.Fprintf(w, "%s  %s\n", indent, describeDiff(d))
		}
	case explain.KindInputsChanged:
		fmt.Fprintf(w, "%stest inputs changed, test binary unchanged%s\n", indent, suffix(r.Detail, len(r.Inputs) > 0))
		for _, c := range r.Inputs {
			fmt.Fprintf(w, "%s  %s\n", indent, describeChange(root, c))
		}
	default:
		fmt.Fprintf(w, "%s%s\n", indent, headline(root, r))
	}
}

// suffix renders the tail of a headline: a colon when detail lines follow,
// or the detail text itself.
func suffix(detail string, more bool) string {
	switch {
	case more:
		return ":"
	case detail != "":
		return ": " + detail
	}
	return ""
}

func headline(root string, r explain.Reason) string {
	switch r.Kind {
	case explain.KindNewPackage:
		return "no earlier record of this test to compare with"
	case explain.KindCacheEntryMissing:
		return "nothing changed, but the cached result is gone: " + r.Detail +
			" (cache not restored, trimmed, or evicted)"
	case explain.KindCachingDisabled:
		return "caching disabled by the test flag " + r.Detail
	case explain.KindLocalDirectoryMode:
		return "go test without package arguments never caches results"
	case explain.KindOutsideModule:
		return "the package is outside any module, GOPATH or GOROOT; go test never caches it"
	case explain.KindTestFailed:
		return "the test failed; failed results are never cached"
	case explain.KindNoEarlierResult:
		return "the last recorded run stored no result (the test failed or caching was disabled), so there was nothing to reuse"
	case explain.KindBuildFailed:
		return "the build failed"
	case explain.KindInputTooNew:
		return displayPath(root, r.Detail) + " was modified less than 2 seconds before go test checked it, so the result was not cached"
	case explain.KindTestCacheCleaned:
		return "the cached result was expired by go clean -testcache"
	case explain.KindCacheCorrupt:
		return "the cache entry could not be read"
	case explain.KindCoverage:
		return "the cached coverage profile is missing or stale: " + r.Detail
	case explain.KindUnknown:
		return "go test printed no reason for this miss"
	}
	return r.Kind.String()
}

func describeDiff(d fingerprint.Diff) string {
	pkg := ids.ImportPath(d.Subject).Base()
	switch d.Kind {
	case fingerprint.DiffPackageChanged:
		if len(d.Files) == 0 {
			return fmt.Sprintf("%s: source files changed", pkg)
		}
		parts := make([]string, 0, len(d.Files))
		for _, f := range d.Files {
			parts = append(parts, f.Name+" "+f.Kind.String())
		}
		return fmt.Sprintf("%s: %s", pkg, strings.Join(parts, ", "))
	case fingerprint.DiffPackageAdded:
		return fmt.Sprintf("now depends on %s", pkg)
	case fingerprint.DiffPackageRemoved:
		return fmt.Sprintf("no longer depends on %s", pkg)
	case fingerprint.DiffModuleChanged:
		return fmt.Sprintf("module %s: %s -> %s", d.Subject, d.Old, d.New)
	case fingerprint.DiffModuleAdded:
		return fmt.Sprintf("new module dependency %s %s", d.Subject, d.New)
	case fingerprint.DiffModuleRemoved:
		return fmt.Sprintf("dropped module dependency %s %s", d.Subject, d.Old)
	case fingerprint.DiffSettingChanged:
		return fmt.Sprintf("%s: %s -> %s", d.Subject, orUnset(d.Old), orUnset(d.New))
	}
	return d.Kind.String() + " " + d.Subject
}

func orUnset(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}

func describeChange(root string, c inputs.Change) string {
	if c.Input.Op == testlog.OpGetenv {
		switch c.Kind {
		case inputs.ChangeEnvSet:
			return "env " + c.Input.Name + ": now set"
		case inputs.ChangeEnvUnset:
			return "env " + c.Input.Name + ": now unset"
		default:
			return "env " + c.Input.Name + ": value changed"
		}
	}
	path := displayPath(root, c.Input.Name)
	if c.IsDir {
		path += "/"
	}
	span := ""
	if !c.OldTime.IsZero() {
		span = fmt.Sprintf(" (%s -> %s)", formatTime(c.OldTime), formatTime(c.NewTime))
	}
	switch c.Kind {
	case inputs.ChangeCreated:
		return path + ": created"
	case inputs.ChangeDeleted:
		return path + ": deleted"
	case inputs.ChangeContent:
		return path + ": content changed"
	case inputs.ChangeMTimeOnly:
		if c.IsDir {
			return path + ": mtime changed, entries identical" + span
		}
		return path + ": mtime changed, content identical" + span
	case inputs.ChangeMTime:
		return path + ": mtime changed, content not compared (larger than the content limit)" + span
	case inputs.ChangeMode:
		return path + ": file mode changed"
	case inputs.ChangeListing:
		return path + ": listing changed: " + describeEntries(c.Entries)
	case inputs.ChangeAccess:
		return path + ": still unreadable, with a different error"
	}
	return path + ": " + c.Kind.String()
}

func describeEntries(entries []inputs.EntryChange) string {
	groups := []struct {
		kind  inputs.ChangeKind
		label string
	}{
		{inputs.ChangeCreated, "added"},
		{inputs.ChangeDeleted, "removed"},
		{inputs.ChangeContent, "content changed"},
		{inputs.ChangeMode, "mode changed"},
		{inputs.ChangeMTime, "new mtime, content not compared"},
		{inputs.ChangeMTimeOnly, "new mtime, same content"},
	}
	var parts []string
	for _, g := range groups {
		var names []string
		for _, e := range entries {
			if e.Kind == g.kind {
				name := e.Name
				if e.IsDir {
					name += "/"
				}
				names = append(names, name)
			}
		}
		if len(names) > 0 {
			parts = append(parts, g.label+" "+strings.Join(names, ", "))
		}
	}
	return strings.Join(parts, "; ")
}

// displayPath shows paths inside the module relative to its root, with
// forward slashes, so reports read the same on every OS.
func displayPath(root, path string) string {
	if root != "" && testlog.Within(root, path) {
		if rel, err := filepath.Rel(root, path); err == nil {
			return filepath.ToSlash(rel)
		}
	}
	return path
}

func writeNote(w io.Writer, n explain.Note) {
	switch n.Kind {
	case explain.NoteNoPreviousState:
		fmt.Fprintf(w, "note: no earlier state at\n"+
			"        %s\n"+
			"      Either this is the first run with uncached, or the state (kept inside GOCACHE\n"+
			"      by default) was not restored between runs.\n", n.Detail)
	case explain.NoteTimestampsOnly:
		fmt.Fprintf(w, "note: %d %s missed the cache only because file mtimes changed while the content stayed\n"+
			"      the same. A fresh git checkout does exactly this. Run `uncached restore-mtime` before\n"+
			"      go test so that mtimes match between checkouts.\n", n.Count, plural(n.Count, "package", "packages"))
	case explain.NoteRemoteCache:
		fmt.Fprintln(w, "note: GOCACHEPROG is set. uncached cannot read input lists from a cache program,\n"+
			"      so reasons about changed files and variables are missing.")
	case explain.NoteUnrecognizedOutput:
		fmt.Fprintf(w, "note: %d test cache debug %s not recognized; this Go version may print messages\n"+
			"      uncached does not know. First one: %s\n", n.Count, plural(n.Count, "line was", "lines were"), n.Detail)
	case explain.NoteAnalysisFailed:
		fmt.Fprintf(w, "note: the analysis is incomplete: %s\n", n.Detail)
	default:
		fmt.Fprintf(w, "note: %s %s\n", n.Kind, n.Detail)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
