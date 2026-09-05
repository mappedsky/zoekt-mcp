package gitrepo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func plumbingHash(t *testing.T, hash string) plumbing.Hash {
	t.Helper()
	return plumbing.NewHash(hash)
}

// fixture builds a repository under root with two commits and one tag, and
// returns its path.
func fixture(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "repo")
	repository, err := git.PlainInit(path, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	tree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}

	commit := func(name, content, message string, when time.Time) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(path, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := tree.Add(name); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
		hash, err := tree.Commit(message, &git.CommitOptions{
			Author: &object.Signature{Name: "Test", Email: "test@example.com", When: when},
		})
		if err != nil {
			t.Fatalf("commit: %v", err)
		}
		return hash.String()
	}

	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	commit("requirements.txt", "pyyaml==6.0\n", "Add requirements", base)
	commit("README.md", "# fixture\n", "Add README", base.Add(time.Hour))
	head := commit("requirements.txt", "pyyaml==6.0.2\n", "Bump pyyaml", base.Add(2*time.Hour))

	if _, err := repository.CreateTag("v1.0.0", plumbingHash(t, head), nil); err != nil {
		t.Fatalf("CreateTag: %v", err)
	}
	return path
}

func newFixtureStore(t *testing.T) (*Store, string) {
	t.Helper()
	root := t.TempDir()
	path := fixture(t, root)
	store, err := NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store, path
}

func TestNewStoreRejectsUnusableRoots(t *testing.T) {
	for _, root := range []string{"", "   ", "relative/path"} {
		if _, err := NewStore(root); err == nil {
			t.Fatalf("NewStore(%q) = nil error; want an error", root)
		}
	}
}

// The clone path comes from the index, so confining it is what stops a
// surprising value from becoming an arbitrary filesystem read.
func TestStoreRefusesPathsOutsideItsRoot(t *testing.T) {
	store, _ := newFixtureStore(t)
	for _, path := range []string{"/etc", filepath.Join(store.Root(), "..", "elsewhere"), "relative"} {
		if _, err := store.Log(path, LogOptions{Limit: 1}); err == nil {
			t.Fatalf("Log(%q) = nil error; want it refused", path)
		}
	}
}

func TestLogReturnsNewestFirst(t *testing.T) {
	store, path := newFixtureStore(t)
	commits, err := store.Log(path, LogOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("commits = %d; want 3", len(commits))
	}
	if commits[0].Subject != "Bump pyyaml" {
		t.Fatalf("first subject = %q; want the newest commit", commits[0].Subject)
	}
	if commits[0].Author != "Test" || commits[0].Email != "test@example.com" {
		t.Fatalf("author = %s <%s>; want the fixture signature", commits[0].Author, commits[0].Email)
	}
}

// The path filter is what answers "has this manifest changed", which is the
// question a dependency finding actually raises.
func TestLogFiltersByPath(t *testing.T) {
	store, path := newFixtureStore(t)
	commits, err := store.Log(path, LogOptions{Path: "requirements.txt", Limit: 10})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("commits = %d; want the 2 touching requirements.txt", len(commits))
	}
	for _, commit := range commits {
		if commit.Subject == "Add README" {
			t.Fatal("a commit that did not touch the path was returned")
		}
	}
}

func TestLogHonoursTheLimit(t *testing.T) {
	store, path := newFixtureStore(t)
	commits, err := store.Log(path, LogOptions{Limit: 1})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("commits = %d; want 1", len(commits))
	}
}

func TestShowReportsChurnAndOmitsThePatchByDefault(t *testing.T) {
	store, path := newFixtureStore(t)
	commit, files, patch, truncated, err := store.Show(path, "HEAD", false, 1024)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if commit.Subject != "Bump pyyaml" {
		t.Fatalf("subject = %q", commit.Subject)
	}
	if len(files) != 1 || files[0].Path != "requirements.txt" {
		t.Fatalf("files = %+v; want just requirements.txt", files)
	}
	if patch != "" || truncated {
		t.Fatalf("patch returned without include_patch: %q", patch)
	}
}

func TestShowTruncatesALargePatch(t *testing.T) {
	store, path := newFixtureStore(t)
	_, _, patch, truncated, err := store.Show(path, "HEAD", true, 10)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if !truncated || len(patch) != 10 {
		t.Fatalf("patch = %d bytes truncated=%v; want it cut to 10", len(patch), truncated)
	}
}

// A root commit has no parent; reporting it beats failing, since it is a real
// state a history walk reaches.
func TestShowHandlesTheRootCommit(t *testing.T) {
	store, path := newFixtureStore(t)
	commits, err := store.Log(path, LogOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	root := commits[len(commits)-1]
	_, files, _, _, err := store.Show(path, root.Hash, false, 1024)
	if err != nil {
		t.Fatalf("Show(root): %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %+v; want the file the root commit added", files)
	}
}

func TestDiffBetweenTagAndHead(t *testing.T) {
	store, path := newFixtureStore(t)
	commits, err := store.Log(path, LogOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	files, _, _, err := store.Diff(path, commits[len(commits)-1].Hash, "v1.0.0", false, 1024)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("diff reported no changed files")
	}
}

func TestBlameAttributesLinesAndBoundsTheRange(t *testing.T) {
	store, path := newFixtureStore(t)
	lines, total, err := store.Blame(path, "", "requirements.txt", 1, 1)
	if err != nil {
		t.Fatalf("Blame: %v", err)
	}
	if total != 1 {
		t.Fatalf("total lines = %d; want 1", total)
	}
	if len(lines) != 1 || lines[0].Line != 1 {
		t.Fatalf("lines = %+v; want line 1", lines)
	}
	if lines[0].Author == "" || lines[0].Hash == "" {
		t.Fatalf("line not attributed: %+v", lines[0])
	}
}

// Reading a file at a tag is the thing the index cannot do, because it only
// covers indexed branches.
func TestFileReadsAtATag(t *testing.T) {
	store, path := newFixtureStore(t)
	content, truncated, err := store.File(path, "v1.0.0", "requirements.txt", 1024)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if content != "pyyaml==6.0.2\n" {
		t.Fatalf("content = %q; want the tagged revision", content)
	}
	if truncated {
		t.Fatal("small file reported truncated")
	}
}

func TestFileTruncatesAtTheByteLimit(t *testing.T) {
	store, path := newFixtureStore(t)
	content, truncated, err := store.File(path, "", "requirements.txt", 4)
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	if !truncated || len(content) != 4 {
		t.Fatalf("content = %q truncated=%v; want it cut to 4 bytes", content, truncated)
	}
}

func TestFileReportsAMissingPathClearly(t *testing.T) {
	store, path := newFixtureStore(t)
	if _, _, err := store.File(path, "", "nope.txt", 1024); err == nil {
		t.Fatal("missing file accepted; want an error")
	}
}

func TestRefsListsBranchesAndTags(t *testing.T) {
	store, path := newFixtureStore(t)
	refs, err := store.Refs(path)
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	kinds := map[string]int{}
	for _, ref := range refs {
		kinds[ref.Kind]++
		if ref.Hash == "" {
			t.Fatalf("ref %s has no hash", ref.Name)
		}
	}
	if kinds["tag"] != 1 {
		t.Fatalf("tags = %d; want 1", kinds["tag"])
	}
	if kinds["branch"] == 0 {
		t.Fatal("no branch refs returned")
	}
}

func TestResolveRejectsAnUnknownRevision(t *testing.T) {
	store, path := newFixtureStore(t)
	if _, err := store.Log(path, LogOptions{Rev: "no-such-ref", Limit: 1}); err == nil {
		t.Fatal("unknown revision accepted; want an error")
	}
}
