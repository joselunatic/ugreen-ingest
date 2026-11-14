package copyengine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCopy_PreservesTreeAndDoesNotModifySource(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	writeFile(t, filepath.Join(src, "DCIM", "100CANON", "IMG_0001.JPG"), "photo-1")
	writeFile(t, filepath.Join(src, "DCIM", "100CANON", "IMG_0002.JPG"), "photo-2")
	writeFile(t, filepath.Join(src, "MISC", "settings.bin"), "settings")

	beforeHash := mustHashTree(t, src)

	var lastProgress Progress
	result, err := Copy(context.Background(), src, dst, func(p Progress) { lastProgress = p })
	if err != nil {
		t.Fatalf("Copy failed: %v", err)
	}

	if result.FileCount != 3 {
		t.Errorf("expected 3 files copied, got %d", result.FileCount)
	}
	if lastProgress.FilesCopied != 3 || lastProgress.FilesTotal != 3 {
		t.Errorf("unexpected final progress: %+v", lastProgress)
	}

	for _, rel := range []string{
		filepath.Join("DCIM", "100CANON", "IMG_0001.JPG"),
		filepath.Join("DCIM", "100CANON", "IMG_0002.JPG"),
		filepath.Join("MISC", "settings.bin"),
	} {
		data, err := os.ReadFile(filepath.Join(dst, rel))
		if err != nil {
			t.Fatalf("destination file %s missing: %v", rel, err)
		}
		srcData, _ := os.ReadFile(filepath.Join(src, rel))
		if string(data) != string(srcData) {
			t.Errorf("content mismatch for %s", rel)
		}
	}

	afterHash := mustHashTree(t, src)
	if beforeHash != afterHash {
		t.Fatal("source tree was modified by Copy")
	}
}

// TestCopy_RespectsCancellation feeds cancel() a specific point in the
// walk (via the progress callback) so the test is deterministic instead of
// racing wall-clock timing.
func TestCopy_RespectsCancellation(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	for i := 0; i < 20; i++ {
		writeFile(t, filepath.Join(src, fmt.Sprintf("file_%02d.bin", i)), strings.Repeat("x", 4096))
	}

	ctx, cancel := context.WithCancel(context.Background())
	filesSeen := 0
	_, err := Copy(ctx, src, dst, func(p Progress) {
		filesSeen++
		if filesSeen == 3 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if filesSeen >= 20 {
		t.Fatal("expected cancellation to stop the copy before all files were processed")
	}
}

// TestCopy_SourceRemovedMidCopy simulates a card pulled out of the reader
// mid-copy (runbook section 18.2): the source vanishes after a few files and
// Copy must return an error rather than a short, "successful" result.
func TestCopy_SourceRemovedMidCopy(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	for i := 0; i < 20; i++ {
		writeFile(t, filepath.Join(src, fmt.Sprintf("file_%02d.bin", i)), strings.Repeat("x", 4096))
	}

	filesSeen := 0
	_, err := Copy(context.Background(), src, dst, func(p Progress) {
		filesSeen++
		if filesSeen == 3 {
			if err := os.RemoveAll(src); err != nil {
				t.Fatal(err)
			}
		}
	})
	if err == nil {
		t.Fatal("expected an error when the source disappears mid-copy")
	}
}

func mustHashTree(t *testing.T, root string) string {
	t.Helper()
	var out string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out += path + ":" + string(data) + ";"
		return nil
	})
	return out
}
