package gotest

import (
	"fmt"
	"slices"
	"strings"

	"github.com/Santaniel0/uncached/internal/ids"
)

// EventKind is one kind of line printed under GODEBUG=gocachetest=1. The
// messages come from tryCacheWithID, computeTestInputsID and saveOutput in
// src/cmd/go/internal/test/test.go (checked against go1.27.3).
type EventKind uint8

const (
	// EventLookup: the test ID was computed and is about to be looked up.
	EventLookup EventKind = iota + 1
	// EventInputsHashed: the input list for the test ID was found and the
	// current values of those inputs were hashed.
	EventInputsHashed
	EventInputListMissing
	EventInputListMalformed
	// EventInputFileError: hashing an input failed, in practice because the
	// file was modified less than two seconds earlier.
	EventInputFileError
	EventOutputMissing
	EventOutputMalformed
	EventOutputExpired
	EventSaved
	EventSaveFailed
	EventCoverageMiss
	EventOutsideModule
)

var eventNames = [...]string{
	EventLookup:             "lookup",
	EventInputsHashed:       "inputs-hashed",
	EventInputListMissing:   "input-list-missing",
	EventInputListMalformed: "input-list-malformed",
	EventInputFileError:     "input-file-error",
	EventOutputMissing:      "output-missing",
	EventOutputMalformed:    "output-malformed",
	EventOutputExpired:      "output-expired",
	EventSaved:              "saved",
	EventSaveFailed:         "save-failed",
	EventCoverageMiss:       "coverage-miss",
	EventOutsideModule:      "outside-module",
}

func (k EventKind) String() string {
	if k >= EventLookup && int(k) < len(eventNames) {
		return eventNames[k]
	}
	return fmt.Sprintf("EventKind(%d)", uint8(k))
}

// tooNewText is errFileTooNew's message in cmd/go.
const tooNewText = "file used as input is too new"

// Event is one parsed debug line about a package.
type Event struct {
	Kind    EventKind
	TestID  ids.ActionID
	InputID ids.ActionID
	// Path is set for EventInputFileError.
	Path string
	// Detail carries the go command's error text, if any.
	Detail string
}

// TooNew reports whether the event is cmd/go refusing an input file that
// was modified within the last two seconds.
func (e Event) TooNew() bool {
	return e.Kind == EventInputFileError && e.Detail == tooNewText
}

// DebugLog collects the test cache debug lines of one go test run.
type DebugLog struct {
	events map[ids.ImportPath][]Event
	// LocalDirMode is set when go test ran without package arguments.
	LocalDirMode bool
	// DisabledBy lists test arguments that turned caching off.
	DisabledBy []string
	// Unrecognized holds lines with the testcache prefix that this version
	// of uncached does not understand.
	Unrecognized []string
}

// NewDebugLog returns an empty log.
func NewDebugLog() *DebugLog {
	return &DebugLog{events: make(map[ids.ImportPath][]Event)}
}

const (
	debugPrefix   = "testcache: "
	outsidePrefix = "caching disabled for package outside of module root, GOPATH, or GOROOT: "
	disabledArg   = "caching disabled for test argument: "
	localDirMode  = "caching disabled in local directory mode"
)

// Line consumes one line of go test's standard error and reports whether it
// was a test cache debug line. Other lines belong to the user.
func (d *DebugLog) Line(line string) bool {
	rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), debugPrefix)
	if !ok {
		return false
	}
	switch {
	case rest == localDirMode:
		d.LocalDirMode = true
	case strings.HasPrefix(rest, disabledArg):
		arg := strings.TrimPrefix(rest, disabledArg)
		if !slices.Contains(d.DisabledBy, arg) {
			d.DisabledBy = append(d.DisabledBy, arg)
		}
	case strings.HasPrefix(rest, outsidePrefix):
		p := ids.ImportPath(strings.TrimPrefix(rest, outsidePrefix))
		d.add(p, Event{Kind: EventOutsideModule})
	default:
		pkg, msg, found := strings.Cut(rest, ": ")
		if !found {
			d.unrecognized(rest)
			break
		}
		ev, ok := parseMessage(msg)
		if !ok {
			d.unrecognized(rest)
			break
		}
		d.add(ids.ImportPath(pkg), ev)
	}
	return true
}

func (d *DebugLog) add(p ids.ImportPath, ev Event) {
	d.events[p] = append(d.events[p], ev)
}

func (d *DebugLog) unrecognized(line string) {
	const keep = 20
	if len(d.Unrecognized) < keep {
		d.Unrecognized = append(d.Unrecognized, line)
	}
}

// Events returns the events for a package in the order they were printed.
func (d *DebugLog) Events(p ids.ImportPath) []Event {
	return d.events[p]
}

func parseMessage(msg string) (Event, bool) {
	switch {
	case strings.HasPrefix(msg, "save test ID "):
		test, input, ok := parseIDChain(strings.TrimPrefix(msg, "save test ID "))
		return Event{Kind: EventSaved, TestID: test, InputID: input}, ok
	case strings.HasPrefix(msg, "test ID "):
		rest := strings.TrimPrefix(msg, "test ID ")
		if strings.Contains(rest, " => input ID ") {
			test, input, ok := parseIDChain(rest)
			return Event{Kind: EventInputsHashed, TestID: test, InputID: input}, ok
		}
		// "test ID <hex of the link action ID string> => <test ID>"
		_, testHex, found := strings.Cut(rest, " => ")
		test, err := ids.ParseActionID(testHex)
		return Event{Kind: EventLookup, TestID: test}, found && err == nil
	case strings.HasPrefix(msg, "input list not found"):
		return Event{Kind: EventInputListMissing, Detail: detail(msg)}, true
	case strings.HasPrefix(msg, "input list malformed"):
		return Event{Kind: EventInputListMalformed, Detail: strings.TrimSpace(strings.TrimPrefix(msg, "input list malformed"))}, true
	case strings.HasPrefix(msg, "input file "):
		path, errText := splitInputFile(strings.TrimPrefix(msg, "input file "))
		return Event{Kind: EventInputFileError, Path: path, Detail: errText}, path != ""
	case strings.HasPrefix(msg, "test output not found"):
		return Event{Kind: EventOutputMissing, Detail: detail(msg)}, true
	case strings.HasPrefix(msg, "test output malformed"):
		return Event{Kind: EventOutputMalformed}, true
	case strings.HasPrefix(msg, "test output expired"):
		return Event{Kind: EventOutputExpired}, true
	case strings.HasPrefix(msg, "reading testlog"):
		return Event{Kind: EventSaveFailed, Detail: detail(msg)}, true
	case strings.HasPrefix(msg, "cached cover profile missing"),
		strings.HasPrefix(msg, "coverage metadata changed"):
		return Event{Kind: EventCoverageMiss, Detail: msg}, true
	}
	return Event{}, false
}

// parseIDChain parses "<test ID> => input ID <input ID> => <output key>".
func parseIDChain(s string) (test, input ids.ActionID, ok bool) {
	testHex, rest, found := strings.Cut(s, " => input ID ")
	if !found {
		return test, input, false
	}
	inputHex, _, found := strings.Cut(rest, " => ")
	if !found {
		return test, input, false
	}
	var err1, err2 error
	test, err1 = ids.ParseActionID(testHex)
	input, err2 = ids.ParseActionID(inputHex)
	return test, input, err1 == nil && err2 == nil
}

// splitInputFile splits "<path>: <error>". Paths may contain ": " in
// principle, so the known error text is matched from the end first.
func splitInputFile(s string) (path, errText string) {
	if p, ok := strings.CutSuffix(s, ": "+tooNewText); ok {
		return p, tooNewText
	}
	if i := strings.LastIndex(s, ": "); i >= 0 {
		return s[:i], s[i+2:]
	}
	return "", ""
}

// detail returns the text after the first ": ", which is the error the go
// command attached to a message.
func detail(msg string) string {
	_, d, _ := strings.Cut(msg, ": ")
	return d
}
