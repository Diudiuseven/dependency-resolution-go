// Package lock persists one resolution as a reproducible lock file and
// verifies installations against it. Lock files contain no timestamps or
// nondeterministic fields and serialize canonically.
package lock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Diudiuseven/dependency-resolution-go/registry"
	"github.com/Diudiuseven/dependency-resolution-go/semver"
	"github.com/Diudiuseven/dependency-resolution-go/solver"
)

// Version is the lock file format version.
const Version = 1

// Unit is one locked package version.
type Unit struct {
	Package string   `json:"package"`
	Version string   `json:"version"`
	Major   uint64   `json:"major"`
	Minor   uint64   `json:"minor"`
	Hash    string   `json:"hash,omitempty"`
	Reasons []Reason `json:"reasons,omitempty"`
}

// Reason is one declaration source recorded inside a Unit.
type Reason struct {
	Kind    string `json:"kind"`
	Member  string `json:"member,omitempty"`
	Package string `json:"from,omitempty"`
	Range   string `json:"range,omitempty"`
	File    string `json:"file,omitempty"`
}

// File is the on-disk lock representation.
type File struct {
	LockVersion int    `json:"lockVersion"`
	InputsHash  string `json:"inputsHash"`
	Units       []Unit `json:"units"`
}

// FromResult builds a lock File from a solve result. Units are sorted by
// package then lane key so output is byte-stable across runs.
func FromResult(res *solver.Result) *File {
	return FromResultWithHashes(res, nil)
}

// FromResultWithHashes fills artifact hashes from reg when it can provide
// the release record; missing records leave the hash empty.
func FromResultWithHashes(res *solver.Result, reg hashSource) *File {
	f := &File{LockVersion: Version, InputsHash: res.InputsHash}
	for _, sel := range res.Selections {
		u := Unit{
			Package: sel.Package,
			Version: sel.Version.String(),
			Major:   sel.Lane.Major,
			Minor:   sel.Lane.Minor,
		}
		if reg != nil {
			if rel, err := reg.Release(context.Background(), sel.Package, sel.Version); err == nil {
				u.Hash = rel.Hash
			}
		}
		for _, s := range sel.Reasons {
			u.Reasons = append(u.Reasons, Reason{
				Kind: s.Kind, Member: s.Member, Package: s.Package_,
				Range: s.Range, File: s.File,
			})
		}
		f.Units = append(f.Units, u)
	}
	sortUnits(f.Units)
	return f
}

func sortUnits(us []Unit) {
	sort.Slice(us, func(i, j int) bool {
		if us[i].Package != us[j].Package {
			return us[i].Package < us[j].Package
		}
		if us[i].Major != us[j].Major {
			return us[i].Major < us[j].Major
		}
		return us[i].Minor < us[j].Minor
	})
}

// hashSource is the subset of registry.Registry used for hash lookup.
type hashSource interface {
	Release(ctx context.Context, pkg string, v semver.Version) (registry.Release, error)
}

// key renders the unit identity package#lane.
func (u Unit) key() string {
	if u.Major == 0 && u.Minor != 0 {
		return fmt.Sprintf("%s#0.%d", u.Package, u.Minor)
	}
	return fmt.Sprintf("%s#%d", u.Package, u.Major)
}

// ID returns a deterministic content identifier of the lock file.
func (f *File) ID() string {
	data, err := f.Marshal()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Marshal serializes the lock file canonically: units sorted, reasons
// sorted, two-space indentation, trailing newline, no HTML escaping.
func (f *File) Marshal() ([]byte, error) {
	cp := *f
	cp.Units = append([]Unit(nil), f.Units...)
	sortUnits(cp.Units)
	for i := range cp.Units {
		if len(cp.Units[i].Reasons) > 1 {
			rs := append([]Reason(nil), cp.Units[i].Reasons...)
			sort.Slice(rs, func(a, b int) bool {
				if rs[a].Package != rs[b].Package {
					return rs[a].Package < rs[b].Package
				}
				return rs[a].Range < rs[b].Range
			})
			cp.Units[i].Reasons = rs
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(&cp); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Unmarshal parses a lock file and validates its shape.
func Unmarshal(data []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse lock file: %w", err)
	}
	if f.LockVersion != Version {
		return nil, fmt.Errorf("unsupported lock version %d (supported: %d)", f.LockVersion, Version)
	}
	return &f, nil
}

// SaveFile atomically writes the lock file to path: it writes a temp file
// in the same directory, fsyncs it, and renames it into place. A crash or
// cancellation therefore never leaves a half-written lock.
func (f *File) SaveFile(path string) error {
	data, err := f.Marshal()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".depresolve-lock-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// LoadFile reads a lock file from path.
func LoadFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Unmarshal(data)
}
