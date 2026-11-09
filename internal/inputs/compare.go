package inputs

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Santaniel0/uncached/internal/testlog"
)

// ChangeKind says how an input differs between two observations.
type ChangeKind uint8

const (
	ChangeEnvSet ChangeKind = iota + 1
	ChangeEnvUnset
	ChangeEnvValue
	ChangeCreated
	ChangeDeleted
	// ChangeContent: size, content, or file type differs.
	ChangeContent
	// ChangeMTimeOnly: the mtime differs and the content is identical.
	ChangeMTimeOnly
	// ChangeMTime: the mtime differs and the content was not compared,
	// because the file is larger than the content limit.
	ChangeMTime
	ChangeMode
	// ChangeListing: entries of an opened directory were added, removed or
	// changed.
	ChangeListing
	// ChangeAccess: the path still cannot be read, but with a different
	// error.
	ChangeAccess
)

var changeNames = [...]string{
	ChangeEnvSet:    "env-set",
	ChangeEnvUnset:  "env-unset",
	ChangeEnvValue:  "env-value",
	ChangeCreated:   "created",
	ChangeDeleted:   "deleted",
	ChangeContent:   "content",
	ChangeMTimeOnly: "mtime-only",
	ChangeMTime:     "mtime",
	ChangeMode:      "mode",
	ChangeListing:   "listing",
	ChangeAccess:    "access",
}

func (k ChangeKind) String() string {
	if k >= ChangeEnvSet && int(k) < len(changeNames) {
		return changeNames[k]
	}
	return fmt.Sprintf("ChangeKind(%d)", uint8(k))
}

// ErrUnknownChange is returned when decoding a change kind fails.
var ErrUnknownChange = errors.New("unknown change kind")

// MarshalText implements encoding.TextMarshaler.
func (k ChangeKind) MarshalText() ([]byte, error) {
	if k < ChangeEnvSet || int(k) >= len(changeNames) {
		return nil, fmt.Errorf("%w: %d", ErrUnknownChange, uint8(k))
	}
	return []byte(k.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (k *ChangeKind) UnmarshalText(b []byte) error {
	for i, name := range changeNames {
		if name != "" && name == string(b) {
			*k = ChangeKind(i)
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrUnknownChange, b)
}

// Change is one difference in one input.
type Change struct {
	Kind    ChangeKind    `json:"kind"`
	Input   testlog.Input `json:"input"`
	IsDir   bool          `json:"is_dir,omitempty"`
	OldTime time.Time     `json:"old_time,omitzero"`
	NewTime time.Time     `json:"new_time,omitzero"`
	// Entries lists the changed entries for ChangeListing.
	Entries []EntryChange `json:"entries,omitempty"`
}

// EntryChange is a change to one entry of a directory listing.
type EntryChange struct {
	Name  string     `json:"name"`
	Kind  ChangeKind `json:"kind"`
	IsDir bool       `json:"is_dir,omitempty"`
}

// TimestampOnly reports whether nothing but modification times changed, as
// far as the recorded digests can tell. This is the signature of a fresh
// checkout, or of a tool that rewrites files with identical content.
func (c Change) TimestampOnly() bool {
	switch c.Kind {
	case ChangeMTimeOnly:
		return true
	case ChangeListing:
		for _, e := range c.Entries {
			if e.Kind != ChangeMTimeOnly {
				return false
			}
		}
		return len(c.Entries) > 0
	}
	return false
}

// Compare lists the differences between two observations of the same input.
func Compare(old, cur Observation) []Change {
	if old.Input != cur.Input {
		return nil
	}
	switch {
	case old.Env != nil && cur.Env != nil:
		if k, changed := compareEnv(*old.Env, *cur.Env); changed {
			return []Change{{Kind: k, Input: cur.Input}}
		}
	case old.File != nil && cur.File != nil:
		return compareFile(cur.Input, *old.File, *cur.File)
	}
	return nil
}

// CompareAll compares each old observation with the current observation of
// the same input. Inputs missing from cur are skipped.
func CompareAll(old []Observation, cur map[testlog.Input]Observation) []Change {
	var out []Change
	for _, o := range old {
		c, ok := cur[o.Input]
		if !ok {
			continue
		}
		out = append(out, Compare(o, c)...)
	}
	return out
}

func compareEnv(o, c EnvState) (ChangeKind, bool) {
	switch {
	case o.Set && !c.Set:
		return ChangeEnvUnset, true
	case !o.Set && c.Set:
		return ChangeEnvSet, true
	case o.Set && o.Digest != c.Digest:
		return ChangeEnvValue, true
	}
	return 0, false
}

func compareFile(in testlog.Input, o, c FileState) []Change {
	base := Change{Input: in, IsDir: o.IsDir || c.IsDir}
	with := func(k ChangeKind) Change {
		ch := base
		ch.Kind = k
		return ch
	}
	switch {
	case o.Exists && !c.Exists:
		return []Change{with(ChangeDeleted)}
	case !o.Exists && c.Exists:
		return []Change{with(ChangeCreated)}
	case !o.Exists:
		if o.Err != c.Err {
			return []Change{with(ChangeAccess)}
		}
		return nil
	case o.IsDir != c.IsDir:
		return []Change{with(ChangeContent)}
	}

	var out []Change
	if o.Mode != c.Mode {
		out = append(out, with(ChangeMode))
	}
	timed := func(k ChangeKind) Change {
		ch := with(k)
		ch.OldTime, ch.NewTime = o.ModTime, c.ModTime
		return ch
	}
	moved := !o.ModTime.Equal(c.ModTime)

	if c.IsDir && in.Op == testlog.OpOpen {
		if entries := compareEntries(o.Entries, c.Entries); len(entries) > 0 {
			ch := timed(ChangeListing)
			ch.Entries = entries
			return append(out, ch)
		}
		if moved || o.Size != c.Size {
			// Same entries with the same metadata: only the directory's
			// own timestamp moved.
			out = append(out, timed(ChangeMTimeOnly))
		}
		return out
	}

	switch {
	case o.Size != c.Size:
		out = append(out, with(ChangeContent))
	case moved:
		out = append(out, timed(classifyMove(o.Digest, c.Digest)))
	}
	return out
}

// classifyMove decides what an mtime change means given content digests.
func classifyMove(oldDigest, curDigest string) ChangeKind {
	switch {
	case oldDigest == "" || curDigest == "":
		return ChangeMTime
	case oldDigest == curDigest:
		return ChangeMTimeOnly
	default:
		return ChangeContent
	}
}

func compareEntries(old, cur []DirEntry) []EntryChange {
	prev := make(map[string]DirEntry, len(old))
	for _, e := range old {
		prev[e.Name] = e
	}
	var out []EntryChange
	for _, c := range cur {
		o, ok := prev[c.Name]
		if !ok {
			out = append(out, EntryChange{Name: c.Name, Kind: ChangeCreated, IsDir: c.IsDir})
			continue
		}
		delete(prev, c.Name)
		switch {
		case o.IsDir != c.IsDir || o.Size != c.Size:
			out = append(out, EntryChange{Name: c.Name, Kind: ChangeContent, IsDir: c.IsDir})
		case o.Mode != c.Mode:
			out = append(out, EntryChange{Name: c.Name, Kind: ChangeMode, IsDir: c.IsDir})
		case !o.ModTime.Equal(c.ModTime):
			out = append(out, EntryChange{Name: c.Name, Kind: classifyMove(o.Digest, c.Digest), IsDir: c.IsDir})
		}
	}
	for _, o := range prev {
		out = append(out, EntryChange{Name: o.Name, Kind: ChangeDeleted, IsDir: o.IsDir})
	}
	slices.SortFunc(out, func(a, b EntryChange) int { return strings.Compare(a.Name, b.Name) })
	return out
}
