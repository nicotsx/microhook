package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestOpenAcceptsRelativePath(t *testing.T) {
	t.Chdir(t.TempDir())

	store, err := Open(context.Background(), "microhook.db")
	if err != nil {
		t.Fatalf("open storage with relative path: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})

	expectedPath, err := filepath.Abs("microhook.db")
	if err != nil {
		t.Fatalf("resolve expected path: %v", err)
	}

	if store.Path() != expectedPath {
		t.Fatalf("expected path %q, got %q", expectedPath, store.Path())
	}
}
