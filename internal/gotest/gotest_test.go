package gotest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Santaniel0/uncached/internal/envvars"
	"github.com/Santaniel0/uncached/internal/ids"
)

// The test binary doubles as a fake go command for Execute: with
// UNCACHED_FAKE_GO set it prints canned output and exits.
func TestMain(m *testing.M) {
	if os.Getenv("UNCACHED_FAKE_GO") == "1" {
		fakeGo()
		return
	}
	os.Exit(m.Run())
}

const (
	testIDHex  = "94e6069ce1d0f227399da99401686a977f21cb037cb6621a40c7d26e79b5f293"
	inputIDHex = "8c6707e4efa58fe15c748834f337b96576379240959ab2e53ed838a732032ab1"
	keyHex     = "dd7d5fc25ce144278e7b5474a5ee488f19964f75bdab97b974c5d4fd3dc8f21d"
)

func fakeGo() {
	fmt.Fprintf(os.Stdout, "args=%s\n", strings.Join(os.Args[1:], " "))
	fmt.Fprintf(os.Stdout, "GODEBUG=%s\n", os.Getenv("GODEBUG"))
	fmt.Fprintf(os.Stderr, "testcache: example.com/a: test ID 3870 => %s\n", testIDHex)
	fmt.Fprintln(os.Stderr, "# example.com/b\nvet: something the user should see")
	fmt.Fprintf(os.Stdout, "ok  \texample.com/a\t(cached)\n")
	fmt.Fprintf(os.Stdout, "FAIL\texample.com/b\t0.10s\n")
	fmt.Fprint(os.Stdout, "no trailing newline")
	os.Exit(3)
}

func TestExecute(t *testing.T) {
	env := envvars.Set(os.Environ(), "UNCACHED_FAKE_GO", "1")
	env = envvars.Set(env, "GODEBUG", "http2debug=1")
	var stdout, stderr bytes.Buffer
	run, err := Execute(context.Background(), Invocation{
		GoBin:  os.Args[0],
		Dir:    t.TempDir(),
		Env:    env,
		Args:   []string{"-run", "X", "./..."},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", run.ExitCode)
	}
	out := stdout.String()
	for _, want := range []string{
		"args=test -run X ./...\n",
		"GODEBUG=http2debug=1,gocachetest=1\n",
		"ok  \texample.com/a\t(cached)\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout is missing %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "no trailing newline") {
		t.Errorf("stdout lost its unterminated last line:\n%q", out)
	}
	if strings.Contains(stderr.String(), "testcache:") {
		t.Errorf("debug lines leaked to stderr:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "vet: something the user should see") {
		t.Errorf("user stderr was dropped:\n%s", stderr.String())
	}
	wantResults := []Result{
		{Package: "example.com/a", Outcome: OutcomeCached},
		{Package: "example.com/b", Outcome: OutcomeFailed},
	}
	if got := run.Results.All(); !slices.Equal(got, wantResults) {
		t.Errorf("results = %v, want %v", got, wantResults)
	}
	evs := run.Debug.Events("example.com/a")
	if len(evs) != 1 || evs[0].Kind != EventLookup || evs[0].TestID.String() != testIDHex {
		t.Errorf("events = %+v", evs)
	}
}

func TestExecuteMissingBinary(t *testing.T) {
	_, err := Execute(context.Background(), Invocation{
		GoBin:  "uncached-no-such-binary",
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
	})
	if err == nil {
		t.Fatal("Execute with a missing binary succeeded")
	}
}

func TestWithCacheDebug(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want string
	}{
		{"unset", []string{"PATH=/bin"}, "gocachetest=1"},
		{"empty", []string{"GODEBUG="}, "gocachetest=1"},
		{"keeps other settings in order", []string{"GODEBUG=b=2,a=1"}, "b=2,a=1,gocachetest=1"},
		{"replaces an existing value", []string{"GODEBUG=gocachetest=0, x=1"}, "x=1,gocachetest=1"},
		{"last duplicate wins", []string{"GODEBUG=a=1", "GODEBUG=b=1"}, "b=1,gocachetest=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WithCacheDebug(tt.env)
			v, _ := envvars.Lookup(got, "GODEBUG")
			if v != tt.want {
				t.Errorf("GODEBUG = %q, want %q", v, tt.want)
			}
			n := 0
			for _, kv := range got {
				if strings.HasPrefix(kv, "GODEBUG=") {
					n++
				}
			}
			if n != 1 {
				t.Errorf("env has %d GODEBUG entries, want 1: %v", n, got)
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		flags     []Flag
		patterns  []string
		listFlags []string
	}{
		{
			name:     "packages only",
			args:     []string{"./..."},
			patterns: []string{"./..."},
		},
		{
			name: "separate flag values are not patterns",
			args: []string{"-run", "TestX", "-tags", "integration", "./pkg", "-count=1"},
			flags: []Flag{
				{"count", "1"},
				{"run", "TestX"},
				{"tags", "integration"},
			},
			patterns:  []string{"./pkg"},
			listFlags: []string{"-tags=integration"},
		},
		{
			name: "bool flags and double dashes",
			args: []string{"--race", "-v", "-json", "-short=false", "./a", "./b"},
			flags: []Flag{
				{"json", "true"},
				{"race", "true"},
				{"short", "false"},
				{"v", "true"},
			},
			patterns:  []string{"./a", "./b"},
			listFlags: []string{"-race=true"},
		},
		{
			name:     "test-prefixed flags and -args",
			args:     []string{"-test.run=X", ".", "-args", "-tags", "zzz"},
			flags:    []Flag{{"run", "X"}},
			patterns: []string{"."},
		},
		{
			name:     "irrelevant value flag is skipped with its value",
			args:     []string{"-coverprofile", "c.out", "-p", "4", "./..."},
			patterns: []string{"./..."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseArgs(tt.args)
			if !slices.Equal(got.Flags, tt.flags) {
				t.Errorf("flags = %v, want %v", got.Flags, tt.flags)
			}
			if !slices.Equal(got.Patterns, tt.patterns) {
				t.Errorf("patterns = %v, want %v", got.Patterns, tt.patterns)
			}
			if lf := got.ListFlags(); !slices.Equal(lf, tt.listFlags) {
				t.Errorf("list flags = %v, want %v", lf, tt.listFlags)
			}
		})
	}
}

func TestResults(t *testing.T) {
	lines := []string{
		"=== RUN   TestX",
		"ok  \texample.com/cached\t(cached)",
		"ok  \texample.com/cover\t(cached)\tcoverage: 50.0% of statements",
		"ok  \texample.com/ran\t0.412s",
		"ok  \texample.com/none\t0.010s [no tests to run]",
		"--- FAIL: TestY (0.00s)",
		"FAIL\texample.com/failed\t0.200s",
		"FAIL\texample.com/broken [build failed]",
		"FAIL\texample.com/setup [setup failed]",
		"?   \texample.com/empty\t[no test files]",
		"FAIL",
		`{"Time":"2026-11-09T10:00:00Z","Action":"output","Package":"example.com/json","Output":"ok  \texample.com/json\t(cached)\n"}`,
		`{"Action":"output","Package":"example.com/json","Test":"TestZ","Output":"ok  \texample.com/fake\t(cached)\n"}`,
		`{"Action":"output","Package":"example.com/jfail","Output":"FAIL\texample.com/jfail\t0.1s\n"}`,
		`{"Action":"pass","Package":"example.com/json"}`,
	}
	r := NewResults()
	for _, l := range lines {
		r.Line(l + "\n")
	}
	want := []Result{
		{"example.com/cached", OutcomeCached},
		{"example.com/cover", OutcomeCached},
		{"example.com/ran", OutcomePassed},
		{"example.com/none", OutcomePassed},
		{"example.com/failed", OutcomeFailed},
		{"example.com/broken", OutcomeBuildFailed},
		{"example.com/setup", OutcomeBuildFailed},
		{"example.com/empty", OutcomeNoTestFiles},
		{"example.com/json", OutcomeCached},
		{"example.com/jfail", OutcomeFailed},
	}
	if got := r.All(); !slices.Equal(got, want) {
		t.Errorf("results:\n got %v\nwant %v", got, want)
	}
}

// Lines below were captured from go1.27.3 on Windows.
func TestDebugLog(t *testing.T) {
	testID := mustID(t, testIDHex)
	inputID := mustID(t, inputIDHex)
	lines := []string{
		"testcache: example.com/probe/a: test ID 3870634a5f5176434d3836375375725869526b75 => " + testIDHex,
		`testcache: example.com/probe/a: input list not found: cache entry not found: open C:\cache\94\` + testIDHex + "-a: The system cannot find the file specified.",
		"testcache: example.com/probe/a: test ID " + testIDHex + " => input ID " + inputIDHex + " => " + keyHex,
		`testcache: example.com/probe/a: test output not found: cache entry not found: open C:\cache\24\x-a: The system cannot find the file specified.`,
		"testcache: example.com/probe/a: save test ID " + testIDHex + " => input ID " + inputIDHex + " => " + keyHex,
		`testcache: example.com/probe/b: input file C:\mod\b\testdata\x.txt: file used as input is too new`,
		"testcache: example.com/probe/b: test output expired due to go clean -testcache",
		"testcache: example.com/probe/b: input list malformed",
		"testcache: caching disabled for test argument: -test.count=1",
		"testcache: caching disabled for test argument: -test.count=1",
		"testcache: caching disabled in local directory mode",
		"testcache: caching disabled for package outside of module root, GOPATH, or GOROOT: command-line-arguments",
		"testcache: example.com/probe/a: something new in a future Go",
		"not a debug line",
	}
	d := NewDebugLog()
	var consumed int
	for _, l := range lines {
		if d.Line(l + "\n") {
			consumed++
		}
	}
	if consumed != len(lines)-1 {
		t.Errorf("consumed %d lines, want %d", consumed, len(lines)-1)
	}
	gotA := d.Events("example.com/probe/a")
	wantA := []Event{
		{Kind: EventLookup, TestID: testID},
		{Kind: EventInputListMissing, Detail: `cache entry not found: open C:\cache\94\` + testIDHex + "-a: The system cannot find the file specified."},
		{Kind: EventInputsHashed, TestID: testID, InputID: inputID},
		{Kind: EventOutputMissing, Detail: `cache entry not found: open C:\cache\24\x-a: The system cannot find the file specified.`},
		{Kind: EventSaved, TestID: testID, InputID: inputID},
	}
	if !slices.Equal(gotA, wantA) {
		t.Errorf("events for a:\n got %+v\nwant %+v", gotA, wantA)
	}
	gotB := d.Events("example.com/probe/b")
	if len(gotB) != 3 || !gotB[0].TooNew() || gotB[0].Path != `C:\mod\b\testdata\x.txt` ||
		gotB[1].Kind != EventOutputExpired || gotB[2].Kind != EventInputListMalformed {
		t.Errorf("events for b: %+v", gotB)
	}
	if !d.LocalDirMode {
		t.Error("local directory mode not detected")
	}
	if !slices.Equal(d.DisabledBy, []string{"-test.count=1"}) {
		t.Errorf("DisabledBy = %v", d.DisabledBy)
	}
	if evs := d.Events("command-line-arguments"); len(evs) != 1 || evs[0].Kind != EventOutsideModule {
		t.Errorf("outside-module events = %+v", evs)
	}
	if len(d.Unrecognized) != 1 {
		t.Errorf("Unrecognized = %v", d.Unrecognized)
	}
}

func mustID(t *testing.T, s string) ids.ActionID {
	t.Helper()
	id, err := ids.ParseActionID(s)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestOutcomeText(t *testing.T) {
	for o := OutcomeUnknown; o <= OutcomeNoTestFiles; o++ {
		b, err := o.MarshalText()
		if err != nil {
			t.Fatal(err)
		}
		var back Outcome
		if err := back.UnmarshalText(b); err != nil || back != o {
			t.Errorf("round trip of %v gave %v, %v", o, back, err)
		}
	}
	var o Outcome
	if err := o.UnmarshalText([]byte("exploded")); err == nil {
		t.Error("unknown outcome name accepted")
	}
}
