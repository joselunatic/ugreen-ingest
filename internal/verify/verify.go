// Package verify re-reads a copied destination tree and compares it against
// the (untouched) source, so an ingest can only be declared VERIFIED after
// every file has actually been checked - never just because Copy() returned
// without error.
package verify

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Hasher is pluggable so the checksum algorithm can change later (the
// runbook allows xxhash for speed) without touching the comparison logic.
type Hasher interface {
	Name() string
	Hash(r io.Reader) (string, error)
}

type sha256Hasher struct{}

func (sha256Hasher) Name() string { return "sha256" }

func (sha256Hasher) Hash(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// NewHasher resolves the configured verification algorithm. Only sha256 is
// implemented today; anything else fails fast at startup rather than
// silently falling back.
func NewHasher(algorithm string) (Hasher, error) {
	switch algorithm {
	case "", "sha256":
		return sha256Hasher{}, nil
	default:
		return nil, fmt.Errorf("unsupported verification algorithm %q (only sha256 is implemented)", algorithm)
	}
}

type Reason string

const (
	ReasonMissing      Reason = "missing_in_destination"
	ReasonSizeMismatch Reason = "size_mismatch"
	ReasonHashMismatch Reason = "hash_mismatch"
)

type Mismatch struct {
	RelPath string
	Reason  Reason
}

func (m Mismatch) String() string {
	return fmt.Sprintf("%s: %s", m.RelPath, m.Reason)
}

type Result struct {
	FilesChecked int64
	Mismatches   []Mismatch
}

func (r Result) OK() bool { return len(r.Mismatches) == 0 }

// Verify walks src (read-only) and, for every file, checks that dst has a
// same-sized file whose content hash matches. It never reads or writes
// anything under src beyond opening files for reading.
func Verify(ctx context.Context, src, dst string, hasher Hasher) (Result, error) {
	var result Result

	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		result.FilesChecked++

		destPath := filepath.Join(dst, rel)
		destInfo, err := os.Stat(destPath)
		if err != nil {
			if os.IsNotExist(err) {
				result.Mismatches = append(result.Mismatches, Mismatch{RelPath: rel, Reason: ReasonMissing})
				return nil
			}
			return err
		}

		srcInfo, err := d.Info()
		if err != nil {
			return err
		}
		if destInfo.Size() != srcInfo.Size() {
			result.Mismatches = append(result.Mismatches, Mismatch{RelPath: rel, Reason: ReasonSizeMismatch})
			return nil
		}

		srcHash, err := hashFile(path, hasher)
		if err != nil {
			return err
		}
		destHash, err := hashFile(destPath, hasher)
		if err != nil {
			return err
		}
		if srcHash != destHash {
			result.Mismatches = append(result.Mismatches, Mismatch{RelPath: rel, Reason: ReasonHashMismatch})
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	return result, nil
}

func hashFile(path string, hasher Hasher) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hasher.Hash(f)
}
