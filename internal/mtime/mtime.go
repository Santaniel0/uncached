// Package mtime sets file modification times from git history, so that a
// fresh checkout of the same commit produces the same mtimes every time.
//
// git does not store mtimes; a clone stamps every file with the time of the
// clone. The go test cache hashes the mtime of every file a test opens, so
// after each fresh checkout those tests miss the cache. Setting each file to
// the time of the last commit that touched it, and each directory to the
// newest time below it, makes mtimes a function of the commit.
package mtime

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Git runs git commands.
type Git interface {
	Output(ctx context.Context, args ...string) ([]byte, error)
	Stream(ctx context.Context, args []string, consume func(io.Reader) error) error
}

// Options select what to restore.
type Options struct {
	// Paths are absolute paths of files or directories to restore. A
	// directory brings along everything tracked below it, because a listing
	// exposes the mtimes of its entries.
	Paths []string
	// All restores every tracked file and directory instead.
	All    bool
	DryRun bool
}

// Result describes what was done.
type Result struct {
	Files   int
	Dirs    int
	Commits int
	// Skipped lists requested paths inside the repository that git does
	// not track.
	Skipped []string
	// Outside counts requested paths outside the repository, such as
	// temporary directories a test changed into.
	Outside int
	// Shallow is set when the repository is a shallow clone.
	Shallow bool
}

// Restore sets mtimes in the repository that contains the git runner's
// working directory.
func Restore(ctx context.Context, git Git, opts Options) (Result, error) {
	var res Result
	out, err := git.Output(ctx, "rev-parse", "--show-toplevel", "--is-shallow-repository")
	if err != nil {
		return res, fmt.Errorf("find repository: %w", err)
	}
	// Line by line, not by fields: the repository path may contain spaces.
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n"), "\n")
	if len(lines) != 2 {
		return res, fmt.Errorf("find repository: unexpected git output %q", out)
	}
	root := filepath.FromSlash(lines[0])
	res.Shallow = lines[1] == "true"

	listed, err := git.Output(ctx, "-C", root, "ls-files", "-z")
	if err != nil {
		return res, fmt.Errorf("list tracked files: %w", err)
	}
	var tracked []string
	for f := range strings.SplitSeq(string(listed), "\x00") {
		if f != "" {
			tracked = append(tracked, f)
		}
	}
	slices.Sort(tracked)

	files, dirs, skipped, outside := selectTargets(root, tracked, opts)
	res.Skipped, res.Outside = skipped, outside
	if len(files) == 0 {
		return res, nil
	}

	times := make(map[string]time.Time, len(files))
	need := make(map[string]bool, len(files))
	for _, f := range files {
		need[f] = true
	}
	err = git.Stream(ctx, []string{"-C", root, "log", "--format=%x01%ct", "--name-only", "-z", "--no-renames"},
		func(r io.Reader) error {
			var perr error
			res.Commits, perr = readLog(r, need, times)
			return perr
		})
	if err != nil {
		return res, fmt.Errorf("read git log: %w", err)
	}

	dirTimes := make(map[string]time.Time, len(dirs))
	for _, d := range dirs {
		dirTimes[d] = time.Time{}
	}
	for f, t := range times {
		for d := path.Dir(f); ; d = path.Dir(d) {
			if cur, ok := dirTimes[d]; ok && t.After(cur) {
				dirTimes[d] = t
			}
			if d == "." {
				break
			}
		}
	}

	for _, f := range files {
		t, ok := times[f]
		if !ok {
			res.Skipped = append(res.Skipped, filepath.Join(root, filepath.FromSlash(f)))
			continue
		}
		done, err := chtimes(filepath.Join(root, filepath.FromSlash(f)), t, opts.DryRun)
		if err != nil {
			return res, err
		}
		if done {
			res.Files++
		}
	}
	// Deepest first, so that nothing touches a directory after it is set.
	slices.SortFunc(dirs, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	for _, d := range dirs {
		t := dirTimes[d]
		if t.IsZero() {
			continue
		}
		done, err := chtimes(filepath.Join(root, filepath.FromSlash(d)), t, opts.DryRun)
		if err != nil {
			return res, err
		}
		if done {
			res.Dirs++
		}
	}
	return res, nil
}

func chtimes(name string, t time.Time, dryRun bool) (bool, error) {
	if dryRun {
		_, err := os.Stat(name)
		return err == nil, nil
	}
	err := os.Chtimes(name, t, t)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // tracked but deleted in the working tree
	}
	if err != nil {
		return false, fmt.Errorf("set mtime: %w", err)
	}
	return true, nil
}

// selectTargets returns the tracked files and directories to restore, as
// slash-separated paths relative to root ("." is root itself), plus the
// requested paths inside root that git does not track and the number of
// requested paths outside root.
func selectTargets(root string, tracked []string, opts Options) (files, dirs, skipped []string, outside int) {
	fileSet := map[string]bool{}
	dirSet := map[string]bool{}
	addFile := func(f string) { fileSet[f] = true }
	// addTree adds dir and everything tracked below it.
	addTree := func(dir string) bool {
		prefix := dir + "/"
		if dir == "." {
			prefix = ""
		}
		start, _ := slices.BinarySearch(tracked, prefix)
		found := false
		for _, f := range tracked[start:] {
			if !strings.HasPrefix(f, prefix) {
				break
			}
			found = true
			addFile(f)
			for d := path.Dir(f); ; d = path.Dir(d) {
				dirSet[d] = true
				if d == dir || d == "." {
					break
				}
			}
		}
		return found
	}

	if opts.All {
		addTree(".")
	} else {
		trackedSet := make(map[string]bool, len(tracked))
		for _, f := range tracked {
			trackedSet[f] = true
		}
		for _, p := range opts.Paths {
			rel, err := filepath.Rel(root, p)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				outside++
				continue
			}
			rel = filepath.ToSlash(rel)
			switch {
			case trackedSet[rel]:
				addFile(rel)
			case addTree(rel):
			default:
				skipped = append(skipped, p)
			}
		}
	}
	files = slices.Sorted(maps.Keys(fileSet))
	dirs = slices.Sorted(maps.Keys(dirSet))
	return files, dirs, skipped, outside
}

// readLog reads `git log --format=%x01%ct --name-only -z` output, newest
// commit first, and records for each needed file the commit time of the
// first commit that names it. It returns as soon as every file is resolved.
func readLog(r io.Reader, need map[string]bool, times map[string]time.Time) (int, error) {
	remaining := len(need)
	br := bufio.NewReaderSize(r, 64<<10)
	var (
		commits int
		current time.Time
	)
	for remaining > 0 {
		tok, err := br.ReadString(0)
		tok = strings.TrimPrefix(strings.TrimSuffix(tok, "\x00"), "\n")
		switch {
		case tok == "":
		case tok[0] == '\x01':
			secs, perr := strconv.ParseInt(strings.TrimSpace(tok[1:]), 10, 64)
			if perr != nil {
				return commits, fmt.Errorf("bad commit time %q", tok[1:])
			}
			current = time.Unix(secs, 0)
			commits++
		case commits > 0 && need[tok]:
			if _, done := times[tok]; !done {
				times[tok] = current
				remaining--
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return commits, err
		}
	}
	return commits, nil
}
