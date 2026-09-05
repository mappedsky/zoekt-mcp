package gitrepo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// The clone path comes from the index rather than from a tool argument, but the
// confinement is what makes that a boundary rather than a convention. No input
// should ever open a repository whose real path is outside the root.
func FuzzOpenStaysInsideTheRoot(f *testing.F) {
	for _, seed := range []string{
		"", ".", "/", "..", "../..", "/etc/passwd", "repo", "/root/repo",
		"/root/../outside", "/root/./repo", "//root//repo", `/root/repo/../../x`,
		"/root/\x00/repo", strings.Repeat("/a", 64),
	} {
		f.Add(seed)
	}

	root := f.TempDir()
	fixture(f, root)
	store, err := NewStore(root)
	if err != nil {
		f.Fatalf("NewStore: %v", err)
	}

	f.Fuzz(func(t *testing.T, path string) {
		// Anchor a relative candidate under the root so the fuzzer spends its
		// budget on traversal shapes rather than on paths that fail trivially.
		candidate := path
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(root, candidate)
		}

		_, err := store.Log(context.Background(), candidate, LogOptions{Limit: 1})
		if err != nil {
			return
		}
		// It opened something. That is only acceptable if the real path is
		// inside the root.
		resolved, resolveErr := filepath.EvalSymlinks(filepath.Clean(candidate))
		if resolveErr != nil {
			t.Fatalf("opened %q but its real path could not be resolved: %v", candidate, resolveErr)
		}
		relative, relErr := filepath.Rel(store.Root(), resolved)
		if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("opened %q, which resolves to %q outside the root %q", candidate, resolved, store.Root())
		}
	})
}

// Line ranges arrive from a model and are not validated upstream, so no
// combination should panic or read outside the file.
func FuzzBlameRangesAreBounded(f *testing.F) {
	for _, seed := range [][2]int{{0, 0}, {1, 1}, {-1, -1}, {1, 1 << 30}, {1 << 30, 1}, {-5, 3}} {
		f.Add(seed[0], seed[1])
	}

	root := f.TempDir()
	path := fixture(f, root)
	store, err := NewStore(root)
	if err != nil {
		f.Fatalf("NewStore: %v", err)
	}

	f.Fuzz(func(t *testing.T, start, end int) {
		lines, total, err := store.Blame(context.Background(), path, "", "requirements.txt", start, end, 0)
		if err != nil {
			return
		}
		if len(lines) > total {
			t.Fatalf("blame returned %d lines for a %d-line file", len(lines), total)
		}
		for _, line := range lines {
			if line.Line < 1 || line.Line > total {
				t.Fatalf("blame returned line %d outside 1..%d", line.Line, total)
			}
		}
	})
}
