// Package inputs records the environment variables, files and directories a
// test consulted, in enough detail to say later which of them changed and
// how.
//
// It records what cmd/go hashes (see hashOpen, hashStat and hashGetenv in
// src/cmd/go/internal/test/test.go): size, mode and modification time for
// paths, the listing for opened directories, and the value of variables. On
// top of that it keeps a digest of what a path holds: the content of small
// regular files, and the entry names of directories. That digest is what
// tells "only the mtime changed" from "the content changed". Variable values
// are only ever stored as SHA-256 digests.
package inputs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/Santaniel0/uncached/internal/testlog"
)

// EnvState is the observed state of an environment variable.
type EnvState struct {
	Set bool `json:"set"`
	// Digest is the hex SHA-256 of the value; the value itself is never kept.
	Digest string `json:"digest,omitempty"`
}

// FileState is the observed state of a path.
type FileState struct {
	Exists  bool        `json:"exists"`
	Err     string      `json:"err,omitempty"`
	IsDir   bool        `json:"is_dir,omitempty"`
	Size    int64       `json:"size,omitempty"`
	Mode    fs.FileMode `json:"mode,omitempty"`
	ModTime time.Time   `json:"mod_time,omitzero"`
	// Digest is the hex SHA-256 of a regular file's content (if it is no
	// larger than the observer's limit) or of a directory's entry names.
	Digest string `json:"digest,omitempty"`
	// Entries is the listing of a directory the test opened.
	Entries []DirEntry `json:"entries,omitempty"`
}

// DirEntry is one entry of an observed directory listing.
type DirEntry struct {
	Name    string      `json:"name"`
	IsDir   bool        `json:"is_dir,omitempty"`
	Size    int64       `json:"size,omitempty"`
	Mode    fs.FileMode `json:"mode"`
	ModTime time.Time   `json:"mod_time"`
	Digest  string      `json:"digest,omitempty"`
}

func (e DirEntry) state() FileState {
	return FileState{Exists: true, IsDir: e.IsDir, Size: e.Size, Mode: e.Mode, ModTime: e.ModTime, Digest: e.Digest}
}

// Observation is the state of one input at one moment.
type Observation struct {
	Input testlog.Input `json:"input"`
	Env   *EnvState     `json:"env,omitempty"`
	File  *FileState    `json:"file,omitempty"`
}

// sameStat reports whether two states agree on everything cmd/go hashes for
// a path, so a digest computed for one is valid for the other.
func sameStat(a, b FileState) bool {
	return a.Exists && b.Exists && a.IsDir == b.IsDir && a.Size == b.Size &&
		a.Mode == b.Mode && a.ModTime.Equal(b.ModTime)
}

// Observer captures the current state of inputs.
type Observer struct {
	// LookupEnv reads the environment the tests run with, which is not
	// necessarily this process's environment.
	LookupEnv func(string) (string, bool)
	// ContentLimit is the largest file whose content is hashed.
	ContentLimit int64
	// Workers bounds concurrent file reads; zero means GOMAXPROCS.
	Workers int
	// Known holds earlier states by path. A path whose size, mode and mtime
	// still match is not read again; its old digest is reused.
	Known map[string]FileState
}

// Observe records the state of each input. The result is in input order.
func (o Observer) Observe(ctx context.Context, ins []testlog.Input) ([]Observation, error) {
	out := make([]Observation, len(ins))
	workers := o.Workers
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	jobs := make(chan int)
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	for range min(workers, max(len(ins), 1)) {
		wg.Go(func() {
			for i := range jobs {
				obs, err := o.observe(ins[i])
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
					continue
				}
				out[i] = obs
			}
		})
	}
	for i := range ins {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (o Observer) observe(in testlog.Input) (Observation, error) {
	obs := Observation{Input: in}
	if in.Op == testlog.OpGetenv {
		v, ok := o.LookupEnv(in.Name)
		st := EnvState{Set: ok}
		if ok {
			st.Digest = digestString(v)
		}
		obs.Env = &st
		return obs, nil
	}
	st, err := o.stat(in.Name)
	if err != nil {
		return Observation{}, err
	}
	if st.Exists && st.IsDir && in.Op == testlog.OpOpen {
		if st.Entries, err = o.list(in.Name); err != nil {
			return Observation{}, err
		}
	}
	obs.File = &st
	return obs, nil
}

func (o Observer) stat(name string) (FileState, error) {
	fi, err := os.Stat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return FileState{}, nil
	}
	if err != nil {
		// cmd/go hashes the error text, so an unreadable path is a state of
		// its own rather than a reason to give up.
		return FileState{Err: err.Error()}, nil
	}
	st := FileState{
		Exists:  true,
		IsDir:   fi.IsDir(),
		Size:    fi.Size(),
		Mode:    fi.Mode(),
		ModTime: fi.ModTime().UTC(),
	}
	if st.Digest, err = o.digest(name, st); err != nil {
		return FileState{}, err
	}
	return st, nil
}

func (o Observer) list(dir string) ([]DirEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}
	out := make([]DirEntry, 0, len(des))
	for _, de := range des {
		fi, err := de.Info()
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed while listing
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", dir, err)
		}
		e := DirEntry{
			Name:    de.Name(),
			IsDir:   fi.IsDir(),
			Size:    fi.Size(),
			Mode:    fi.Mode(),
			ModTime: fi.ModTime().UTC(),
		}
		if e.Digest, err = o.digest(filepath.Join(dir, de.Name()), e.state()); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (o Observer) digest(name string, st FileState) (string, error) {
	if !st.Exists || !(st.IsDir || st.Mode.IsRegular() && st.Size <= o.ContentLimit) {
		return "", nil
	}
	if k, ok := o.Known[name]; ok && k.Digest != "" && sameStat(k, st) {
		return k.Digest, nil
	}
	var (
		d   string
		err error
	)
	if st.IsDir {
		d, err = namesDigest(name)
	} else {
		d, err = contentDigest(name, o.ContentLimit)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", name, err)
	}
	return d, nil
}

func contentDigest(name string, limit int64) (string, error) {
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, limit+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// namesDigest hashes a directory's entry names. A directory's mtime moves
// when entries are added, removed or renamed, so equal digests with
// different mtimes mean only the timestamp changed.
func namesDigest(dir string) (string, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, de := range des {
		name := de.Name()
		if de.IsDir() {
			name += "/"
		}
		io.WriteString(h, name+"\n")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func digestString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// KnownFiles flattens observations, including directory entries, into a
// path-indexed map suitable for Observer.Known.
func KnownFiles(sets ...[]Observation) map[string]FileState {
	known := make(map[string]FileState)
	for _, set := range sets {
		for _, obs := range set {
			if obs.File == nil || !obs.File.Exists {
				continue
			}
			known[obs.Input.Name] = *obs.File
			for _, e := range obs.File.Entries {
				known[filepath.Join(obs.Input.Name, e.Name)] = e.state()
			}
		}
	}
	return known
}
