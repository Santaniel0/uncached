package gotest

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Santaniel0/uncached/internal/ids"
)

// Outcome is how go test finished a package.
type Outcome uint8

const (
	OutcomeUnknown Outcome = iota
	OutcomeCached
	OutcomePassed
	OutcomeFailed
	OutcomeBuildFailed
	OutcomeNoTestFiles
)

var outcomeNames = [...]string{
	OutcomeUnknown:     "unknown",
	OutcomeCached:      "cached",
	OutcomePassed:      "passed",
	OutcomeFailed:      "failed",
	OutcomeBuildFailed: "build-failed",
	OutcomeNoTestFiles: "no-test-files",
}

func (o Outcome) String() string {
	if int(o) < len(outcomeNames) {
		return outcomeNames[o]
	}
	return fmt.Sprintf("Outcome(%d)", uint8(o))
}

// ErrUnknownOutcome is returned when decoding an outcome name fails.
var ErrUnknownOutcome = errors.New("unknown outcome")

// MarshalText implements encoding.TextMarshaler.
func (o Outcome) MarshalText() ([]byte, error) {
	if int(o) >= len(outcomeNames) {
		return nil, fmt.Errorf("%w: %d", ErrUnknownOutcome, uint8(o))
	}
	return []byte(o.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (o *Outcome) UnmarshalText(b []byte) error {
	for i, name := range outcomeNames {
		if name == string(b) {
			*o = Outcome(i)
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrUnknownOutcome, b)
}

// Ran reports whether the test binary ran or was satisfied from the cache,
// as opposed to never getting that far.
func (o Outcome) Ran() bool {
	return o == OutcomeCached || o == OutcomePassed || o == OutcomeFailed
}

// Result is the final status line go test printed for a package.
type Result struct {
	Package ids.ImportPath `json:"package"`
	Outcome Outcome        `json:"outcome"`
}

// Results collects package results from go test's standard output, in
// either the plain or the -json format.
type Results struct {
	byPkg map[ids.ImportPath]Outcome
	order []ids.ImportPath
}

// NewResults returns an empty collector.
func NewResults() *Results {
	return &Results{byPkg: make(map[ids.ImportPath]Outcome)}
}

// The final line for a package, e.g. "ok  \tpkg\t(cached)" or
// "FAIL\tpkg [build failed]".
var resultLine = regexp.MustCompile(`^(ok  |FAIL|\?   )\t(\S+)( \[[a-z ]+ failed\])?(?:\t(.*))?$`)

// testEvent is the subset of a test2json event that carries package results.
type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

// Line feeds one line of go test's standard output.
func (r *Results) Line(line string) {
	line = strings.TrimRight(line, "\r\n")
	if strings.HasPrefix(line, "{") {
		var ev testEvent
		if err := json.Unmarshal([]byte(line), &ev); err == nil && ev.Action != "" {
			if ev.Action == "output" && ev.Test == "" {
				for l := range strings.SplitSeq(ev.Output, "\n") {
					r.plain(strings.TrimRight(l, "\r"))
				}
			}
			return
		}
	}
	r.plain(line)
}

func (r *Results) plain(line string) {
	// test2json mode marks FAIL lines with a leading SYN byte.
	line = strings.TrimPrefix(line, "\x16")
	m := resultLine.FindStringSubmatch(line)
	if m == nil {
		return
	}
	var o Outcome
	switch {
	case m[1] == "ok  " && strings.HasPrefix(m[4], "(cached)"):
		o = OutcomeCached
	case m[1] == "ok  ":
		o = OutcomePassed
	case m[1] == "FAIL" && m[3] != "":
		o = OutcomeBuildFailed
	case m[1] == "FAIL":
		o = OutcomeFailed
	default:
		o = OutcomeNoTestFiles
	}
	p := ids.ImportPath(m[2])
	if _, seen := r.byPkg[p]; !seen {
		r.order = append(r.order, p)
	}
	r.byPkg[p] = o
}

// All returns the results in the order go test printed them.
func (r *Results) All() []Result {
	out := make([]Result, 0, len(r.order))
	for _, p := range r.order {
		out = append(out, Result{Package: p, Outcome: r.byPkg[p]})
	}
	return out
}
