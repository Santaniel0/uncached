package app_test

// These tests run the real go command against small temporary modules. They
// never touch the user's GOCACHE: every go process gets a temporary one.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Santaniel0/uncached/internal/app"
	"github.com/Santaniel0/uncached/internal/envvars"
	"github.com/Santaniel0/uncached/internal/explain"
	"github.com/Santaniel0/uncached/internal/gocache"
	"github.com/Santaniel0/uncached/internal/ids"
	"github.com/Santaniel0/uncached/internal/inputs"
	"github.com/Santaniel0/uncached/internal/state"
)

// sharedCache is a GOCACHE shared by tests that do not clean it, so the
// standard library is compiled once. It is set in TestMain and not changed
// afterwards. Each test uses its own module directory, so test IDs never
// collide.
var sharedCache string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "uncached-test-gocache-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sharedCache = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var (
	t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t2 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
)

var demoFiles = map[string]string{
	"go.mod": "module example.com/demo\n\ngo 1.22\n",
	"money/money.go": `package money

import "fmt"

const symbol = "EUR"

func Format(cents int) string { return fmt.Sprintf("%d.%02d %s", cents/100, cents%100, symbol) }
`,
	"money/money_test.go": `package money

import "testing"

func TestFormat(t *testing.T) {
	if Format(1234) == "" {
		t.Fatal("empty")
	}
}
`,
	"invoice/invoice.go": `package invoice

import "example.com/demo/money"

func Total(items ...int) string {
	sum := 0
	for _, c := range items {
		sum += c
	}
	return "total " + money.Format(sum)
}
`,
	"invoice/invoice_test.go": `package invoice

import (
	"os"
	"strings"
	"testing"
)

func TestTotal(t *testing.T) {
	golden, err := os.ReadFile("testdata/invoice.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(golden), "total") || Total(1, 2) == "" {
		t.Fatal("bad golden file")
	}
}
`,
	"invoice/testdata/invoice.golden": "total 0.03 EUR\n",
	"config/config.go": `package config

import "os"

func Region() string {
	if r := os.Getenv("DEMO_REGION"); r != "" {
		return r
	}
	return "eu"
}
`,
	"config/config_test.go": `package config

import "testing"

func TestRegion(t *testing.T) {
	if Region() == "" {
		t.Fatal("no region")
	}
}
`,
	"catalog/catalog_test.go": `package catalog

import (
	"os"
	"testing"
)

func TestFixtures(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil || len(entries) == 0 {
		t.Fatal("no fixtures", err)
	}
}
`,
	"catalog/testdata/apple.json": `{"name":"apple"}`,
}

type module struct {
	t      *testing.T
	root   string
	tool   *app.Tool
	stdout *bytes.Buffer
	stderr *bytes.Buffer
	opts   app.RunOptions
}

func goBin(t *testing.T) string {
	t.Helper()
	bin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not found in PATH; these tests need the real toolchain")
	}
	return bin
}

// testEnv is the process environment without anything that changes how the
// go command behaves, plus an isolated cache and module setup.
func testEnv(t *testing.T, gocache string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(k)
		if strings.HasPrefix(upper, "GO") || strings.HasPrefix(upper, "CGO_") || upper == "DEMO_REGION" {
			continue
		}
		env = append(env, kv)
	}
	for k, v := range map[string]string{
		"GOCACHE":     gocache,
		"GOPATH":      t.TempDir(),
		"GOENV":       "off",
		"GOFLAGS":     "-mod=mod",
		"GOWORK":      "off",
		"GOTOOLCHAIN": "local",
		"GOPROXY":     "off",
		"CGO_ENABLED": "0",
	} {
		env = envvars.Set(env, k, v)
	}
	return env
}

func newModule(t *testing.T, gocache string, files map[string]string) *module {
	t.Helper()
	bin := goBin(t)
	root := t.TempDir()
	for name, content := range files {
		writeFile(t, filepath.Join(root, filepath.FromSlash(name)), content)
	}
	// Fixtures must be older than two seconds or cmd/go refuses to cache
	// results that read them; pin every fixture to a fixed old time.
	setTimes(t, root, t0)
	m := &module{t: t, root: root, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	m.tool = &app.Tool{
		GoBin:  bin,
		Dir:    root,
		Env:    testEnv(t, gocache),
		Stdout: m.stdout,
		Stderr: m.stderr,
		Now:    func() time.Time { return t2 },
	}
	m.opts.ReportJSON = filepath.Join(t.TempDir(), "report.json")
	return m
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setTimes(t *testing.T, root string, when time.Time) {
	t.Helper()
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		return os.Chtimes(p, when, when)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func (m *module) path(name string) string { return filepath.Join(m.root, filepath.FromSlash(name)) }

func (m *module) chtimes(name string, when time.Time) {
	m.t.Helper()
	if err := os.Chtimes(m.path(name), when, when); err != nil {
		m.t.Fatal(err)
	}
}

// run runs `uncached run` and returns the report it saved and go test's
// exit code.
func (m *module) run(args ...string) (explain.Report, int) {
	m.t.Helper()
	m.stdout.Reset()
	m.stderr.Reset()
	opts := m.opts
	opts.Args = args
	code, err := m.tool.Run(context.Background(), opts)
	if err != nil {
		m.t.Fatalf("Run: %v\nstderr:\n%s", err, m.stderr)
	}
	data, err := os.ReadFile(opts.ReportJSON)
	if err != nil {
		m.t.Fatal(err)
	}
	var rep explain.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		m.t.Fatal(err)
	}
	return rep, code
}

func reasonKinds(rep explain.Report) map[string][]explain.Kind {
	out := map[string][]explain.Kind{}
	for _, p := range rep.Packages {
		name := strings.TrimPrefix(string(p.Package), "example.com/demo/")
		out[name] = nil
		for _, r := range p.Reasons {
			out[name] = append(out[name], r.Kind)
		}
	}
	return out
}

func (m *module) expect(rep explain.Report, want map[string][]explain.Kind) {
	m.t.Helper()
	got := reasonKinds(rep)
	if len(got) != len(want) {
		m.t.Fatalf("report covers %v, want %v\nstderr:\n%s", got, want, m.stderr)
	}
	for p, k := range want {
		if !slices.Equal(got[p], k) {
			m.t.Errorf("%s: reasons %v, want %v\nstderr:\n%s", p, got[p], k, m.stderr)
		}
	}
}

func reason(t *testing.T, rep explain.Report, pkg string) explain.Reason {
	t.Helper()
	for _, p := range rep.Packages {
		if p.Package == ids.ImportPath("example.com/demo/"+pkg) && len(p.Reasons) > 0 {
			return p.Reasons[0]
		}
	}
	t.Fatalf("no reason for %s", pkg)
	return explain.Reason{}
}

const (
	newPkg        = explain.KindNewPackage
	changedInputs = explain.KindInputsChanged
	changedBinary = explain.KindBinaryChanged
	disabled      = explain.KindCachingDisabled
)

func TestRunExplainsMisses(t *testing.T) {
	t.Parallel()
	m := newModule(t, sharedCache, demoFiles)
	m.opts.StatePath = filepath.Join(t.TempDir(), "state.json")

	rep, code := m.run("./...")
	if code != 0 {
		t.Fatalf("exit code %d\n%s%s", code, m.stdout, m.stderr)
	}
	m.expect(rep, map[string][]explain.Kind{"catalog": {newPkg}, "config": {newPkg}, "invoice": {newPkg}, "money": {newPkg}})
	if len(rep.Notes) == 0 || rep.Notes[0].Kind != explain.NoteNoPreviousState {
		t.Errorf("first run notes = %+v", rep.Notes)
	}
	if !strings.Contains(m.stdout.String(), "ok  \texample.com/demo/money") {
		t.Errorf("go test output was not passed through:\n%s", m.stdout)
	}

	t.Run("everything cached", func(t *testing.T) {
		rep, _ := m.run("./...")
		m.expect(rep, map[string][]explain.Kind{"catalog": nil, "config": nil, "invoice": nil, "money": nil})
		if !strings.Contains(m.stderr.String(), "uncached: 4 of 4 packages cached") {
			t.Errorf("stderr:\n%s", m.stderr)
		}
	})

	t.Run("mtime-only change", func(t *testing.T) {
		m.chtimes("invoice/testdata/invoice.golden", t1)
		rep, _ := m.run("./...")
		m.expect(rep, map[string][]explain.Kind{"catalog": nil, "config": nil, "invoice": {changedInputs}, "money": nil})
		r := reason(t, rep, "invoice")
		if len(r.Inputs) != 1 || r.Inputs[0].Kind != inputs.ChangeMTimeOnly || !r.Inputs[0].OldTime.Equal(t0) || !r.Inputs[0].NewTime.Equal(t1) {
			t.Errorf("invoice changes = %+v", r.Inputs)
		}
		if !slices.ContainsFunc(rep.Notes, func(n explain.Note) bool { return n.Kind == explain.NoteTimestampsOnly }) {
			t.Errorf("notes = %+v", rep.Notes)
		}
	})

	t.Run("environment change", func(t *testing.T) {
		const secret = "region-value-that-must-not-leak"
		m.tool.Env = envvars.Set(m.tool.Env, "DEMO_REGION", secret)
		rep, _ := m.run("./...")
		m.expect(rep, map[string][]explain.Kind{"catalog": nil, "config": {changedInputs}, "invoice": nil, "money": nil})
		r := reason(t, rep, "config")
		if len(r.Inputs) != 1 || r.Inputs[0].Kind != inputs.ChangeEnvSet || r.Inputs[0].Input.Name != "DEMO_REGION" {
			t.Errorf("config changes = %+v", r.Inputs)
		}
		saved, err := os.ReadFile(m.opts.StatePath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(m.stderr.String(), secret) || strings.Contains(string(saved), secret) {
			t.Error("the variable's value leaked into the report or the state file")
		}
	})

	t.Run("content change", func(t *testing.T) {
		writeFile(t, m.path("invoice/testdata/invoice.golden"), "total 0.03 USD\n")
		m.chtimes("invoice/testdata/invoice.golden", t2)
		rep, _ := m.run("./...")
		r := reason(t, rep, "invoice")
		if len(r.Inputs) != 1 || r.Inputs[0].Kind != inputs.ChangeContent {
			t.Errorf("invoice changes = %+v", r.Inputs)
		}
	})

	t.Run("directory listing change", func(t *testing.T) {
		writeFile(t, m.path("catalog/testdata/pear.json"), `{"name":"pear"}`)
		m.chtimes("catalog/testdata/pear.json", t0)
		m.chtimes("catalog/testdata", t1)
		rep, _ := m.run("./...")
		m.expect(rep, map[string][]explain.Kind{"catalog": {changedInputs}, "config": nil, "invoice": nil, "money": nil})
		r := reason(t, rep, "catalog")
		if len(r.Inputs) != 1 || r.Inputs[0].Kind != inputs.ChangeListing ||
			!slices.Equal(r.Inputs[0].Entries, []inputs.EntryChange{{Name: "pear.json", Kind: inputs.ChangeCreated}}) {
			t.Errorf("catalog changes = %+v", r.Inputs)
		}
	})

	t.Run("source change in a dependency", func(t *testing.T) {
		src := strings.Replace(demoFiles["money/money.go"], `"EUR"`, `"USD"`, 1)
		writeFile(t, m.path("money/money.go"), src)
		rep, _ := m.run("./...")
		m.expect(rep, map[string][]explain.Kind{"catalog": nil, "config": nil, "invoice": {changedBinary}, "money": {changedBinary}})
		r := reason(t, rep, "invoice")
		if len(r.Binary) != 1 || r.Binary[0].Subject != "example.com/demo/money" ||
			len(r.Binary[0].Files) != 1 || r.Binary[0].Files[0].Name != "money.go" {
			t.Errorf("invoice binary diffs = %+v", r.Binary)
		}
	})

	t.Run("caching disabled by -count=1", func(t *testing.T) {
		rep, _ := m.run("-count=1", "./...")
		m.expect(rep, map[string][]explain.Kind{"catalog": {disabled}, "config": {disabled}, "invoice": {disabled}, "money": {disabled}})
		if r := reason(t, rep, "money"); r.Detail != "-count=1" {
			t.Errorf("detail = %q", r.Detail)
		}
	})

	t.Run("records survive a run with caching disabled", func(t *testing.T) {
		rep, _ := m.run("./...")
		m.expect(rep, map[string][]explain.Kind{"catalog": nil, "config": nil, "invoice": nil, "money": nil})
	})
}

func TestRunKeepsExitCodeOfFailingTests(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"go.mod":          "module example.com/demo\n\ngo 1.22\n",
		"bad/bad_test.go": "package bad\n\nimport \"testing\"\n\nfunc TestBad(t *testing.T) { t.Fatal(\"boom\") }\n",
	}
	m := newModule(t, sharedCache, files)
	rep, code := m.run("./...")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	m.expect(rep, map[string][]explain.Kind{"bad": {newPkg, explain.KindTestFailed}})

	writeFile(t, m.path("bad/bad_test.go"), "package bad\n\nimport \"testing\"\n\nfunc TestBad(t *testing.T) {}\n")
	rep, code = m.run("./...")
	if code != 0 {
		t.Errorf("exit code after the fix = %d, want 0", code)
	}
	r := reason(t, rep, "bad")
	if r.Kind != changedBinary || len(r.Binary) != 1 || len(r.Binary[0].Files) != 1 || r.Binary[0].Files[0].Name != "bad_test.go" {
		t.Errorf("after the fix: %+v", r)
	}
}

func TestRunReportsInputTooNew(t *testing.T) {
	t.Parallel()
	m := newModule(t, sharedCache, demoFiles)
	m.run("./invoice")
	// A future mtime is "less than two seconds old" to cmd/go no matter how
	// slow the machine is, which keeps this test deterministic.
	m.chtimes("invoice/testdata/invoice.golden", time.Now().Add(time.Hour))
	rep, _ := m.run("./invoice")
	got := reasonKinds(rep)["invoice"]
	if !slices.Contains(got, explain.KindInputTooNew) {
		t.Fatalf("reasons = %v\n%s", got, m.stderr)
	}
	if !strings.Contains(m.stderr.String(), "invoice/testdata/invoice.golden was modified less than 2 seconds") {
		t.Errorf("stderr:\n%s", m.stderr)
	}
}

func TestRunReportsEvictedEntry(t *testing.T) {
	t.Parallel()
	m := newModule(t, sharedCache, demoFiles)
	m.opts.StatePath = filepath.Join(t.TempDir(), "state.json")
	m.run("./money")
	st, err := state.Load(m.opts.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	rec := st.Tests["example.com/demo/money"]
	if rec == nil || len(rec.TestIDs) == 0 {
		t.Fatalf("no test IDs recorded: %+v", rec)
	}
	cache, err := gocache.Open(sharedCache)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range rec.TestIDs {
		if err := os.Remove(cache.EntryFile(id)); err != nil {
			t.Fatal(err)
		}
	}
	rep, _ := m.run("./money")
	m.expect(rep, map[string][]explain.Kind{"money": {explain.KindCacheEntryMissing}})
}

func TestRunReportsCleanedTestCache(t *testing.T) {
	t.Parallel()
	// go clean -testcache expires every result in a cache, so this test gets
	// a cache of its own.
	own := t.TempDir()
	m := newModule(t, own, demoFiles)
	m.run("./money")
	clean := exec.Command(m.tool.GoBin, "clean", "-testcache")
	clean.Dir = m.root
	clean.Env = m.tool.Env
	if out, err := clean.CombinedOutput(); err != nil {
		t.Fatalf("go clean -testcache: %v\n%s", err, out)
	}
	rep, _ := m.run("./money")
	m.expect(rep, map[string][]explain.Kind{"money": {explain.KindTestCacheCleaned}})
}

func TestRunWithJSONOutput(t *testing.T) {
	t.Parallel()
	m := newModule(t, sharedCache, demoFiles)
	m.run("-json", "./money")
	rep, _ := m.run("-json", "./money")
	m.expect(rep, map[string][]explain.Kind{"money": nil})
	for _, line := range strings.Split(strings.TrimSpace(m.stdout.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("stdout is not pure test2json output; line %q", line)
		}
	}
}

func TestExplainAndDiff(t *testing.T) {
	t.Parallel()
	m := newModule(t, sharedCache, demoFiles)
	stateDir := t.TempDir()
	m.opts.StatePath = filepath.Join(stateDir, "state.json")
	m.run("./invoice")
	before, err := os.ReadFile(m.opts.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	oldState := filepath.Join(stateDir, "old.json")
	writeFile(t, oldState, string(before))

	m.chtimes("invoice/testdata/invoice.golden", t1)
	m.run("./invoice")

	var out bytes.Buffer
	m.tool.Stdout = &out
	if err := m.tool.Explain(context.Background(), app.ExplainOptions{StatePath: m.opts.StatePath}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "invoice/testdata/invoice.golden: mtime changed, content identical") {
		t.Errorf("explain output:\n%s", out.String())
	}

	out.Reset()
	if err := m.tool.Diff(app.DiffOptions{OldPath: oldState, NewPath: m.opts.StatePath}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "0 of 1 test packages unchanged") ||
		!strings.Contains(out.String(), "invoice/testdata/invoice.golden: mtime changed, content identical") {
		t.Errorf("diff output:\n%s", out.String())
	}
}

func TestRestoreMtimeWithoutStateIsNotAnError(t *testing.T) {
	t.Parallel()
	m := newModule(t, sharedCache, demoFiles)
	var out bytes.Buffer
	m.tool.Stdout = &out
	err := m.tool.RestoreMtime(context.Background(), app.RestoreOptions{StatePath: filepath.Join(t.TempDir(), "none.json")})
	if err != nil {
		t.Fatalf("RestoreMtime without state: %v", err)
	}
	if !strings.Contains(out.String(), "nothing to restore yet") {
		t.Errorf("output: %s", out.String())
	}
}

func TestExplainWithoutReport(t *testing.T) {
	t.Parallel()
	m := newModule(t, sharedCache, demoFiles)
	err := m.tool.Explain(context.Background(), app.ExplainOptions{StatePath: filepath.Join(t.TempDir(), "none.json")})
	if err == nil || !strings.Contains(err.Error(), "run `uncached run` first") {
		t.Errorf("err = %v", err)
	}
}

// TestRestoreMtimeAcrossCheckouts walks through the CI scenario: every job
// starts from a fresh clone, which gives all files a new mtime.
func TestRestoreMtimeAcrossCheckouts(t *testing.T) {
	t.Parallel()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH")
	}
	m := newModule(t, sharedCache, demoFiles)
	m.tool.GitBin = gitBin
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	writeFile(t, cfg, "")
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": cfg, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "test", "GIT_AUTHOR_EMAIL": "test@example.com",
		"GIT_COMMITTER_NAME": "test", "GIT_COMMITTER_EMAIL": "test@example.com",
		"GIT_AUTHOR_DATE": t1.Format(time.RFC3339), "GIT_COMMITTER_DATE": t1.Format(time.RFC3339),
	} {
		m.tool.Env = envvars.Set(m.tool.Env, k, v)
	}
	for _, args := range [][]string{{"init", "-q", "."}, {"add", "-A"}, {"commit", "-q", "-m", "demo"}} {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = m.root
		cmd.Env = m.tool.Env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	freshCheckout := func(when time.Time) { setTimes(t, m.root, when) }
	restore := func() {
		var out bytes.Buffer
		m.tool.Stdout = &out
		if err := m.tool.RestoreMtime(context.Background(), app.RestoreOptions{}); err != nil {
			t.Fatal(err)
		}
		m.tool.Stdout = m.stdout
		if !strings.Contains(out.String(), "restored mtimes of 2 files and 1 directory") {
			t.Errorf("restore-mtime output: %s", out.String())
		}
	}

	m.run("./...")
	freshCheckout(t2)
	rep, _ := m.run("./...")
	if got := reasonKinds(rep); !slices.Equal(got["invoice"], []explain.Kind{changedInputs}) || !slices.Equal(got["catalog"], []explain.Kind{changedInputs}) {
		t.Fatalf("after a fresh checkout: %v", got)
	}

	// The first restore moves mtimes to commit time, which still differs
	// from what the cached results were recorded with.
	freshCheckout(t2.Add(time.Hour))
	restore()
	m.run("./...")

	// From now on every fresh checkout plus restore yields cache hits.
	freshCheckout(t2.Add(2 * time.Hour))
	restore()
	rep, _ = m.run("./...")
	m.expect(rep, map[string][]explain.Kind{"catalog": nil, "config": nil, "invoice": nil, "money": nil})
}
