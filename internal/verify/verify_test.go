package verify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ugreen-ingest/ugreen-ingest/internal/copyengine"
)

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setupCopiedCard(t *testing.T) (src, dst string) {
	t.Helper()
	src = t.TempDir()
	dst = t.TempDir()
	mustWrite(t, filepath.Join(src, "clip_001.mov"), "video-bytes-aaaaaaaaaaaa")
	mustWrite(t, filepath.Join(src, "sub", "clip_002.mov"), "video-bytes-bbbbbbbbbbbb")

	if _, err := copyengine.Copy(context.Background(), src, dst, nil); err != nil {
		t.Fatalf("setup copy failed: %v", err)
	}
	return src, dst
}

func TestVerify_Success(t *testing.T) {
	src, dst := setupCopiedCard(t)
	hasher, _ := NewHasher("sha256")

	result, err := Verify(context.Background(), src, dst, hasher)
	if err != nil {
		t.Fatalf("Verify error: %v", err)
	}
	if !result.OK() {
		t.Fatalf("expected OK, got mismatches: %v", result.Mismatches)
	}
	if result.FilesChecked != 2 {
		t.Errorf("expected 2 files checked, got %d", result.FilesChecked)
	}
}

// TestVerify_CorruptedDestination is the negative test required by
// RUNBOOK_MOY_MEDIA_INGEST.md section 18.1: a deliberate corruption of the
// destination must be detected, never silently accepted.
func TestVerify_CorruptedDestination(t *testing.T) {
	src, dst := setupCopiedCard(t)
	hasher, _ := NewHasher("sha256")

	corrupted := filepath.Join(dst, "clip_001.mov")
	if err := os.WriteFile(corrupted, []byte("TAMPERED-BYTES-DIFFERENT-LEN!!"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Verify(context.Background(), src, dst, hasher)
	if err != nil {
		t.Fatalf("Verify error: %v", err)
	}
	if result.OK() {
		t.Fatal("expected verification to fail on corrupted destination, got OK")
	}
	found := false
	for _, m := range result.Mismatches {
		if m.RelPath == "clip_001.mov" && (m.Reason == ReasonHashMismatch || m.Reason == ReasonSizeMismatch) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected mismatch for clip_001.mov, got %v", result.Mismatches)
	}
}

func TestVerify_MissingFile(t *testing.T) {
	src, dst := setupCopiedCard(t)
	hasher, _ := NewHasher("sha256")

	if err := os.Remove(filepath.Join(dst, "sub", "clip_002.mov")); err != nil {
		t.Fatal(err)
	}

	result, err := Verify(context.Background(), src, dst, hasher)
	if err != nil {
		t.Fatalf("Verify error: %v", err)
	}
	if result.OK() {
		t.Fatal("expected verification to fail on missing file, got OK")
	}
	if result.Mismatches[0].Reason != ReasonMissing {
		t.Errorf("expected missing_in_destination, got %s", result.Mismatches[0].Reason)
	}
}

func TestVerify_SameSizeContentTamper(t *testing.T) {
	src, dst := setupCopiedCard(t)
	hasher, _ := NewHasher("sha256")

	original := "video-bytes-aaaaaaaaaaaa"
	tampered := "video-bytes-ZZZZZZZZZZZZ" // same length, different content
	if len(original) != len(tampered) {
		t.Fatal("test fixture invariant broken")
	}
	if err := os.WriteFile(filepath.Join(dst, "clip_001.mov"), []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Verify(context.Background(), src, dst, hasher)
	if err != nil {
		t.Fatalf("Verify error: %v", err)
	}
	if result.OK() {
		t.Fatal("expected hash mismatch for same-size tampered content, got OK")
	}
	if result.Mismatches[0].Reason != ReasonHashMismatch {
		t.Errorf("expected hash_mismatch, got %s", result.Mismatches[0].Reason)
	}
}

// TestVerify_RespectsCancellation uses a pre-cancelled context: ctx.Err()
// is checked before any file is hashed, so this deterministically confirms
// Verify never mistakes a cancelled run for a completed one.
func TestVerify_RespectsCancellation(t *testing.T) {
	src, dst := setupCopiedCard(t)
	hasher, _ := NewHasher("sha256")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Verify(ctx, src, dst, hasher)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestNewHasher_RejectsUnsupportedAlgorithm(t *testing.T) {
	if _, err := NewHasher("xxhash"); err == nil {
		t.Fatal("expected error for unimplemented algorithm, got nil")
	}
}
