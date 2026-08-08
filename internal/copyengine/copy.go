// Package copyengine copies a source directory tree to a destination,
// preserving relative structure and file timestamps. The source is only
// ever opened read-only.
package copyengine

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

type Progress struct {
	FilesCopied int64
	FilesTotal  int64
	BytesCopied int64
	BytesTotal  int64
}

type ProgressFunc func(Progress)

type Result struct {
	FileCount  int64
	BytesTotal int64
}

// Plan walks src and returns the total file count and byte size, without
// copying anything. The ingest manager uses this at detection time to know
// how much work a card represents.
func Plan(src string) (Result, error) {
	var r Result
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		r.FileCount++
		r.BytesTotal += info.Size()
		return nil
	})
	return r, err
}

// Copy recursively copies every file under src into dst, mirroring the
// relative directory structure. It never modifies src. onProgress (optional)
// is called after each file with a running total.
func Copy(ctx context.Context, src, dst string, onProgress ProgressFunc) (Result, error) {
	plan, err := Plan(src)
	if err != nil {
		return Result{}, fmt.Errorf("scan source: %w", err)
	}

	progress := Progress{FilesTotal: plan.FileCount, BytesTotal: plan.BytesTotal}

	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}

		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		destPath := filepath.Join(dst, rel)

		if d.IsDir() {
			return os.MkdirAll(destPath, 0o755)
		}

		if err := copyFile(path, destPath); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		progress.FilesCopied++
		progress.BytesCopied += info.Size()
		if onProgress != nil {
			onProgress(progress)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}

	return Result{FileCount: progress.FilesCopied, BytesTotal: progress.BytesCopied}, nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}

	buf := make([]byte, 1<<20) // 1 MiB
	if _, err := io.CopyBuffer(out, in, buf); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	// Best-effort: preserve the original modification time.
	_ = os.Chtimes(dst, info.ModTime(), info.ModTime())
	return nil
}
