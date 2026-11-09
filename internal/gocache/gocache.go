// Package gocache reads entries from the go command's on-disk build cache.
//
// The layout follows src/cmd/go/internal/cache/cache.go in the Go
// repository (checked against go1.27.3). An action entry lives at
// <dir>/<xx>/<action id>-a and holds one line
//
//	v1 <action id> <output id> <size, %20d> <unix nanoseconds, %20d>\n
//
// where xx is the first byte of the ID in hex. The output it points to lives
// at <dir>/<yy>/<output id>-d.
package gocache

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Santaniel0/uncached/internal/ids"
)

const (
	hexSize   = 64
	entrySize = 2 + 1 + hexSize + 1 + hexSize + 1 + 20 + 1 + 20 + 1

	// Test logs are a few kilobytes; anything this large is not one.
	maxOutput = 64 << 20
)

var (
	// ErrNotFound means the cache has no entry, or no output, for an ID.
	ErrNotFound = errors.New("not in the go build cache")
	// ErrCorrupt means an entry exists but cannot be parsed or does not
	// match its output.
	ErrCorrupt = errors.New("corrupt go build cache entry")
)

// Dir is a read-only view of a GOCACHE directory.
type Dir struct {
	root string
}

// Open returns a view of the cache rooted at root.
func Open(root string) (*Dir, error) {
	fi, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("open go build cache: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("open go build cache: %s is not a directory", root)
	}
	return &Dir{root: root}, nil
}

// Entry is a parsed action entry.
type Entry struct {
	Size int64
	Time time.Time
	out  [32]byte
}

// EntryFile returns the path of the action entry file for id. It exists
// mainly so tests can simulate an evicted entry.
func (d *Dir) EntryFile(id ids.ActionID) string {
	return d.file(id, "a")
}

func (d *Dir) file(id [32]byte, suffix string) string {
	h := hex.EncodeToString(id[:])
	return filepath.Join(d.root, h[:2], h+"-"+suffix)
}

// Entry reads the action entry for id.
func (d *Dir) Entry(id ids.ActionID) (Entry, error) {
	f, err := os.Open(d.EntryFile(id))
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, fmt.Errorf("action %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return Entry{}, fmt.Errorf("action %s: %w", id, err)
	}
	defer f.Close()

	// One extra byte tells a correctly sized entry from a longer file.
	buf := make([]byte, entrySize+1)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return Entry{}, fmt.Errorf("action %s: %w", id, err)
	}
	if n != entrySize {
		return Entry{}, fmt.Errorf("action %s: %w: entry has %d bytes, want %d", id, ErrCorrupt, n, entrySize)
	}
	return parseEntry(id, buf[:entrySize])
}

func parseEntry(id ids.ActionID, b []byte) (Entry, error) {
	bad := func(what string) (Entry, error) {
		return Entry{}, fmt.Errorf("action %s: %w: %s", id, ErrCorrupt, what)
	}
	const (
		idStart   = 3
		outStart  = idStart + hexSize + 1
		sizeStart = outStart + hexSize + 1
		timeStart = sizeStart + 20 + 1
	)
	if string(b[:idStart]) != "v1 " || b[outStart-1] != ' ' || b[sizeStart-1] != ' ' ||
		b[timeStart-1] != ' ' || b[entrySize-1] != '\n' {
		return bad("unexpected layout")
	}
	var got [32]byte
	if _, err := hex.Decode(got[:], b[idStart:idStart+hexSize]); err != nil || got != id {
		return bad("action ID does not match file name")
	}
	var e Entry
	if _, err := hex.Decode(e.out[:], b[outStart:outStart+hexSize]); err != nil {
		return bad("bad output ID")
	}
	size, err := strconv.ParseInt(strings.TrimSpace(string(b[sizeStart:sizeStart+20])), 10, 64)
	if err != nil || size < 0 {
		return bad("bad size")
	}
	nano, err := strconv.ParseInt(strings.TrimSpace(string(b[timeStart:timeStart+20])), 10, 64)
	if err != nil || nano < 0 {
		return bad("bad timestamp")
	}
	e.Size = size
	e.Time = time.Unix(0, nano).UTC()
	return e, nil
}

// Read returns the output stored under id together with its entry.
func (d *Dir) Read(id ids.ActionID) ([]byte, Entry, error) {
	e, err := d.Entry(id)
	if err != nil {
		return nil, Entry{}, err
	}
	if e.Size > maxOutput {
		return nil, Entry{}, fmt.Errorf("action %s: output is %d bytes, refusing to read more than %d", id, e.Size, maxOutput)
	}
	if e.Size == 0 {
		return []byte{}, e, nil
	}
	data, err := os.ReadFile(d.file(e.out, "d"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, Entry{}, fmt.Errorf("action %s: output file: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, Entry{}, fmt.Errorf("action %s: %w", id, err)
	}
	if int64(len(data)) != e.Size {
		return nil, Entry{}, fmt.Errorf("action %s: %w: output has %d bytes, entry says %d", id, ErrCorrupt, len(data), e.Size)
	}
	return data, e, nil
}
