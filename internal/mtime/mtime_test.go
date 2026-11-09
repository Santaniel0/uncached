package mtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Santaniel0/uncached/internal/envvars"
	"github.com/Santaniel0/uncached/internal/gocmd"
)

var (
	jan    = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	feb    = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	mar    = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	future = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
)

type repo struct {
	t    *testing.T
	root string
	git  gocmd.Command
}

// newRepo creates a git repository isolated from the user's git config.
func newRepo(t *testing.T) *repo {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found in PATH; skipping tests that need a real repository")
	}
	root := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	env := os.Environ()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   cfg,
		"GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME":     "test",
		"GIT_AUTHOR_EMAIL":    "test@example.com",
		"GIT_COMMITTER_NAME":  "test",
		"GIT_COMMITTER_EMAIL": "test@example.com",
	} {
		env = envvars.Set(env, k, v)
	}
	r := &repo{t: t, root: root, git: gocmd.Command{Bin: bin, Dir: root, Env: env}}
	r.run("init", "-q", ".")
	return r
}

func (r *repo) run(args ...string) string {
	r.t.Helper()
	out, err := r.git.Output(context.Background(), args...)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(out)
}

func (r *repo) commit(when time.Time, files map[string]string) {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			r.t.Fatal(err)
		}
	}
	saved := r.git.Env
	r.git.Env = envvars.Set(envvars.Set(saved, "GIT_COMMITTER_DATE", when.Format(time.RFC3339)), "GIT_AUTHOR_DATE", when.Format(time.RFC3339))
	defer func() { r.git.Env = saved }()
	r.run("add", "-A")
	r.run("commit", "-q", "-m", "change")
}

func (r *repo) path(name string) string { return filepath.Join(r.root, filepath.FromSlash(name)) }

// scramble sets every file and directory to one far-future time, which is
// what a fresh clone looks like to the go command.
func (r *repo) scramble() {
	r.t.Helper()
	err := filepath.WalkDir(r.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		return os.Chtimes(p, future, future)
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) mtime(name string) time.Time {
	r.t.Helper()
	fi, err := os.Stat(r.path(name))
	if err != nil {
		r.t.Fatal(err)
	}
	return fi.ModTime()
}

func populate(t *testing.T) *repo {
	r := newRepo(t)
	r.commit(jan, map[string]string{"a/testdata/x.txt": "x", "a/a.go": "package a", "b/z.txt": "z"})
	r.commit(feb, map[string]string{"a/testdata/sub/y.txt": "y"})
	r.commit(mar, map[string]string{"b/z.txt": "z2"})
	r.scramble()
	return r
}

func TestRestoreSelectedPaths(t *testing.T) {
	r := populate(t)
	res, err := Restore(context.Background(), r.git, Options{Paths: []string{
		r.path("a/testdata"),
		r.path("b/z.txt"),
		r.path("generated.txt"),
		filepath.Join(filepath.Dir(r.root), "elsewhere"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{
		"a/testdata/x.txt":     jan,
		"a/testdata/sub/y.txt": feb,
		"a/testdata/sub":       feb,
		"a/testdata":           feb,
		"b/z.txt":              mar,
		"a":                    future,
		"a/a.go":               future,
		"b":                    future,
	}
	for name, w := range want {
		if got := r.mtime(name); !got.Equal(w) {
			t.Errorf("%s: mtime %v, want %v", name, got.UTC(), w)
		}
	}
	if res.Files != 3 || res.Dirs != 2 {
		t.Errorf("restored %d files and %d dirs, want 3 and 2", res.Files, res.Dirs)
	}
	if res.Commits != 3 {
		t.Errorf("read %d commits, want 3", res.Commits)
	}
	if len(res.Skipped) != 1 || res.Outside != 1 {
		t.Errorf("skipped = %v, outside = %d; want the untracked path skipped and one outside", res.Skipped, res.Outside)
	}
	if res.Shallow {
		t.Error("a full repository reported as shallow")
	}
}

func TestRestoreStopsReadingHistoryEarly(t *testing.T) {
	r := populate(t)
	res, err := Restore(context.Background(), r.git, Options{Paths: []string{r.path("b/z.txt")}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Commits != 1 {
		t.Errorf("read %d commits, want 1: the newest commit already names the file", res.Commits)
	}
	if !r.mtime("b/z.txt").Equal(mar) {
		t.Errorf("b/z.txt mtime = %v", r.mtime("b/z.txt"))
	}
}

func TestRestoreAll(t *testing.T) {
	r := populate(t)
	res, err := Restore(context.Background(), r.git, Options{All: true})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{
		"a/a.go": jan,
		"a":      feb,
		"b":      mar,
		".":      mar,
	}
	for name, w := range want {
		if got := r.mtime(name); !got.Equal(w) {
			t.Errorf("%s: mtime %v, want %v", name, got.UTC(), w)
		}
	}
	if res.Files != 4 {
		t.Errorf("restored %d files, want 4", res.Files)
	}
}

func TestRestoreDryRun(t *testing.T) {
	r := populate(t)
	res, err := Restore(context.Background(), r.git, Options{All: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Files != 4 || !r.mtime("b/z.txt").Equal(future) {
		t.Errorf("dry run: files=%d, b/z.txt mtime %v", res.Files, r.mtime("b/z.txt"))
	}
}

func TestReadLog(t *testing.T) {
	log := "\x011772323200\x00\nb/z.txt\x00\x011769904000\x00\na/new name.txt\x00b/z.txt\x00\x011767225600\x00\x011767225500\x00\na/x.txt\x00"
	need := map[string]bool{"b/z.txt": true, "a/new name.txt": true, "a/x.txt": true, "never.txt": true}
	times := map[string]time.Time{}
	commits, err := readLog(strings.NewReader(log), need, times)
	if err != nil {
		t.Fatal(err)
	}
	if commits != 4 {
		t.Errorf("commits = %d, want 4 (an empty commit counts)", commits)
	}
	want := map[string]int64{"b/z.txt": 1772323200, "a/new name.txt": 1769904000, "a/x.txt": 1767225500}
	for f, secs := range want {
		if got := times[f]; got.Unix() != secs {
			t.Errorf("%s: time %d, want %d", f, got.Unix(), secs)
		}
	}
	if _, ok := times["never.txt"]; ok {
		t.Error("a file missing from history got a time")
	}

	if _, err := readLog(strings.NewReader("\x01soon\x00"), map[string]bool{"x": true}, map[string]time.Time{}); err == nil {
		t.Error("a malformed commit time was accepted")
	}
}

func TestSelectTargets(t *testing.T) {
	root := filepath.FromSlash("/repo")
	tracked := []string{"a/b/c.txt", "a/b/d/e.txt", "a/bb.txt", "z.txt"}
	files, dirs, skipped, outside := selectTargets(root, tracked, Options{Paths: []string{
		filepath.FromSlash("/repo/a/b"),
		filepath.FromSlash("/repo/z.txt"),
		filepath.FromSlash("/repo/a/missing"),
		filepath.FromSlash("/tmp/x"),
	}})
	if outside != 1 {
		t.Errorf("outside = %d, want 1", outside)
	}
	if want := []string{"a/b/c.txt", "a/b/d/e.txt", "z.txt"}; !slices.Equal(files, want) {
		t.Errorf("files = %v, want %v", files, want)
	}
	if want := []string{"a/b", "a/b/d"}; !slices.Equal(dirs, want) {
		t.Errorf("dirs = %v, want %v (a/bb.txt shares a prefix but is not inside a/b)", dirs, want)
	}
	if len(skipped) != 1 {
		t.Errorf("skipped = %v", skipped)
	}
}
