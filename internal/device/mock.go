package device

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
)

// MockProvider treats every immediate subdirectory of Root as an inserted
// card, per RUNBOOK_MOY_MEDIA_INGEST.md section 12.2. Creating a directory
// simulates inserting a card; nothing here ever writes to it.
type MockProvider struct {
	Root string
}

func NewMockProvider(root string) *MockProvider {
	return &MockProvider{Root: root}
}

func (m *MockProvider) Name() string { return "mock" }

func (m *MockProvider) Scan(ctx context.Context) ([]Source, error) {
	entries, err := os.ReadDir(m.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sources []Source
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(m.Root, e.Name())
		size, count, err := scanTree(path)
		if err != nil {
			// Unreadable right now (e.g. still being written by a test
			// fixture) - skip, it will be picked up on the next scan.
			continue
		}
		sources = append(sources, Source{
			Identifier: e.Name(),
			Label:      e.Name(),
			Path:       path,
			SizeBytes:  size,
			FileCount:  count,
		})
	}
	return sources, nil
}

func scanTree(root string) (size int64, count int64, err error) {
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		size += info.Size()
		count++
		return nil
	})
	return size, count, err
}
