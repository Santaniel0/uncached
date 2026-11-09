package explain

import (
	"errors"
	"fmt"
)

// Kind is the reason a test result did not come from the cache.
type Kind uint8

const (
	// KindNewPackage: nothing was recorded for this test before.
	KindNewPackage Kind = iota + 1
	// KindBinaryChanged: the test binary or its arguments differ from the
	// recorded run.
	KindBinaryChanged
	// KindInputsChanged: same binary, but files or variables the test read
	// changed.
	KindInputsChanged
	// KindCacheEntryMissing: nothing changed, but GOCACHE no longer has the
	// entry (not restored, trimmed, or evicted).
	KindCacheEntryMissing
	// KindCachingDisabled: a test flag such as -count=1 turns caching off.
	KindCachingDisabled
	// KindLocalDirectoryMode: go test without package arguments.
	KindLocalDirectoryMode
	// KindOutsideModule: the package is outside any module, GOPATH or
	// GOROOT.
	KindOutsideModule
	// KindTestFailed: failed results are never stored.
	KindTestFailed
	// KindNoEarlierResult: the last recorded run stored no result (it failed,
	// or caching was disabled), so there was nothing to reuse.
	KindNoEarlierResult
	KindBuildFailed
	// KindInputTooNew: an input was modified less than two seconds before
	// cmd/go checked it.
	KindInputTooNew
	// KindTestCacheCleaned: go clean -testcache expired the result.
	KindTestCacheCleaned
	KindCacheCorrupt
	// KindCoverage: a cached coverage profile is missing or stale.
	KindCoverage
	// KindUnknown: go test's debug output gave no reason.
	KindUnknown
)

var kindNames = [...]string{
	KindNewPackage:         "new-package",
	KindBinaryChanged:      "binary-changed",
	KindInputsChanged:      "inputs-changed",
	KindCacheEntryMissing:  "cache-entry-missing",
	KindCachingDisabled:    "caching-disabled",
	KindLocalDirectoryMode: "local-directory-mode",
	KindOutsideModule:      "outside-module",
	KindTestFailed:         "test-failed",
	KindNoEarlierResult:    "no-earlier-result",
	KindBuildFailed:        "build-failed",
	KindInputTooNew:        "input-too-new",
	KindTestCacheCleaned:   "testcache-cleaned",
	KindCacheCorrupt:       "cache-corrupt",
	KindCoverage:           "coverage",
	KindUnknown:            "unknown",
}

func (k Kind) String() string { return name(kindNames[:], int(k), "Kind") }

// MarshalText implements encoding.TextMarshaler.
func (k Kind) MarshalText() ([]byte, error) { return marshal(kindNames[:], int(k)) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (k *Kind) UnmarshalText(b []byte) error {
	i, err := unmarshal(kindNames[:], b)
	*k = Kind(i)
	return err
}

// NoteKind is a run-level observation printed after the per-package
// reasons.
type NoteKind uint8

const (
	// NoteNoPreviousState: there was nothing to compare with.
	NoteNoPreviousState NoteKind = iota + 1
	// NoteTimestampsOnly: some packages missed only because mtimes moved.
	NoteTimestampsOnly
	// NoteRemoteCache: GOCACHEPROG is set, so input lists are unreadable.
	NoteRemoteCache
	// NoteUnrecognizedOutput: go test printed debug lines this version of
	// uncached does not understand.
	NoteUnrecognizedOutput
	// NoteAnalysisFailed: part of the analysis failed; reasons may be
	// incomplete.
	NoteAnalysisFailed
)

var noteNames = [...]string{
	NoteNoPreviousState:    "no-previous-state",
	NoteTimestampsOnly:     "timestamps-only",
	NoteRemoteCache:        "remote-cache",
	NoteUnrecognizedOutput: "unrecognized-output",
	NoteAnalysisFailed:     "analysis-failed",
}

func (k NoteKind) String() string { return name(noteNames[:], int(k), "NoteKind") }

// MarshalText implements encoding.TextMarshaler.
func (k NoteKind) MarshalText() ([]byte, error) { return marshal(noteNames[:], int(k)) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (k *NoteKind) UnmarshalText(b []byte) error {
	i, err := unmarshal(noteNames[:], b)
	*k = NoteKind(i)
	return err
}

// Mode says whether a report explains a go test run or compares two saved
// states.
type Mode uint8

const (
	ModeRun Mode = iota + 1
	ModeDiff
)

var modeNames = [...]string{ModeRun: "run", ModeDiff: "diff"}

func (m Mode) String() string { return name(modeNames[:], int(m), "Mode") }

// MarshalText implements encoding.TextMarshaler.
func (m Mode) MarshalText() ([]byte, error) { return marshal(modeNames[:], int(m)) }

// UnmarshalText implements encoding.TextUnmarshaler.
func (m *Mode) UnmarshalText(b []byte) error {
	i, err := unmarshal(modeNames[:], b)
	*m = Mode(i)
	return err
}

// ErrUnknownName is returned when decoding an unknown enum name.
var ErrUnknownName = errors.New("unknown name")

func name(names []string, i int, typ string) string {
	if i > 0 && i < len(names) {
		return names[i]
	}
	return fmt.Sprintf("%s(%d)", typ, i)
}

func marshal(names []string, i int) ([]byte, error) {
	if i <= 0 || i >= len(names) {
		return nil, fmt.Errorf("%w: %d", ErrUnknownName, i)
	}
	return []byte(names[i]), nil
}

func unmarshal(names []string, b []byte) (int, error) {
	for i, n := range names {
		if i > 0 && n == string(b) {
			return i, nil
		}
	}
	return 0, fmt.Errorf("%w: %q", ErrUnknownName, b)
}
