// Package gitrepo reads history out of the bare clones a code index was built
// from, using go-git so the binary stays static and never shells out.
//
// Nothing here writes. The repositories are opened read-only and may be mounted
// read-only; go-git also performs no ownership check, so a sidecar running as a
// different user than the process that cloned them still works.
package gitrepo

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Store opens repositories beneath a fixed root.
type Store struct {
	root string
}

// NewStore returns a store that will only open paths under root. The index
// supplies those paths, so confining them is what keeps a surprising value
// from turning into an arbitrary filesystem read.
func NewStore(root string) (*Store, error) {
	cleaned := filepath.Clean(strings.TrimSpace(root))
	if cleaned == "" || cleaned == "." {
		return nil, fmt.Errorf("repository root must not be empty")
	}
	if !filepath.IsAbs(cleaned) {
		return nil, fmt.Errorf("repository root must be an absolute path, got %q", root)
	}
	return &Store{root: cleaned}, nil
}

// Root is the directory every opened repository must live under.
func (s *Store) Root() string { return s.root }

func (s *Store) open(path string) (*git.Repository, error) {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return nil, fmt.Errorf("repository path must be absolute, got %q", path)
	}
	relative, err := filepath.Rel(s.root, cleaned)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("repository path %q is outside the configured root %q", path, s.root)
	}
	repository, err := git.PlainOpen(cleaned)
	if err != nil {
		return nil, fmt.Errorf("open repository at %s: %w", cleaned, err)
	}
	return repository, nil
}

// Commit is one revision, without its diff.
type Commit struct {
	Hash    string    `json:"hash" jsonschema:"Full commit SHA."`
	Short   string    `json:"short" jsonschema:"Abbreviated commit SHA."`
	Author  string    `json:"author" jsonschema:"Author name."`
	Email   string    `json:"email" jsonschema:"Author email."`
	When    time.Time `json:"when" jsonschema:"Author date."`
	Subject string    `json:"subject" jsonschema:"First line of the commit message."`
	Body    string    `json:"body,omitempty" jsonschema:"Remainder of the commit message."`
	Parents []string  `json:"parents,omitempty" jsonschema:"Abbreviated parent SHAs."`
}

// FileStat is one file's churn within a commit or diff.
type FileStat struct {
	Path      string `json:"path" jsonschema:"File path."`
	Additions int    `json:"additions" jsonschema:"Lines added."`
	Deletions int    `json:"deletions" jsonschema:"Lines removed."`
}

// BlameLine attributes one line of a file.
type BlameLine struct {
	Line   int       `json:"line" jsonschema:"1-based line number."`
	Hash   string    `json:"hash" jsonschema:"Abbreviated SHA of the commit that last touched the line."`
	Author string    `json:"author" jsonschema:"Author of that commit."`
	When   time.Time `json:"when" jsonschema:"Date of that commit."`
	Text   string    `json:"text" jsonschema:"The line itself."`
}

// Ref is one branch or tag.
type Ref struct {
	Name string    `json:"name" jsonschema:"Short ref name."`
	Kind string    `json:"kind" jsonschema:"Either branch or tag."`
	Hash string    `json:"hash" jsonschema:"Abbreviated SHA the ref points at."`
	When time.Time `json:"when,omitempty" jsonschema:"Date of the commit the ref points at."`
}

// LogOptions bounds a history walk.
type LogOptions struct {
	Rev   string
	Path  string
	Limit int
}

// Log returns commits reachable from a revision, newest first, optionally
// restricted to those touching one path.
func (s *Store) Log(path string, opts LogOptions) ([]Commit, error) {
	repository, err := s.open(path)
	if err != nil {
		return nil, err
	}
	start, err := resolve(repository, opts.Rev)
	if err != nil {
		return nil, err
	}

	logOptions := &git.LogOptions{From: start}
	if trimmed := strings.TrimSpace(opts.Path); trimmed != "" {
		logOptions.FileName = &trimmed
	}
	iter, err := repository.Log(logOptions)
	if err != nil {
		return nil, fmt.Errorf("read log: %w", err)
	}
	defer iter.Close()

	commits := make([]Commit, 0, opts.Limit)
	err = iter.ForEach(func(commit *object.Commit) error {
		if len(commits) >= opts.Limit {
			return storeStopIteration
		}
		commits = append(commits, convertCommit(commit))
		return nil
	})
	if err != nil && err != storeStopIteration {
		return nil, fmt.Errorf("walk log: %w", err)
	}
	return commits, nil
}

// Show returns one commit with its per-file churn, and its patch when asked.
func (s *Store) Show(path, rev string, includePatch bool, maxPatchBytes int) (Commit, []FileStat, string, bool, error) {
	repository, err := s.open(path)
	if err != nil {
		return Commit{}, nil, "", false, err
	}
	hash, err := resolve(repository, rev)
	if err != nil {
		return Commit{}, nil, "", false, err
	}
	commit, err := repository.CommitObject(hash)
	if err != nil {
		return Commit{}, nil, "", false, fmt.Errorf("read commit %s: %w", hash, err)
	}

	// A root commit has no parent to diff against; report it with no churn
	// rather than failing, since that is a real and reachable state.
	var parent *object.Commit
	if commit.NumParents() > 0 {
		if parent, err = commit.Parent(0); err != nil {
			return Commit{}, nil, "", false, fmt.Errorf("read parent of %s: %w", hash, err)
		}
	}
	stats, patch, truncated, err := diffCommits(parent, commit, includePatch, maxPatchBytes)
	if err != nil {
		return Commit{}, nil, "", false, err
	}
	return convertCommit(commit), stats, patch, truncated, nil
}

// Diff compares two revisions.
func (s *Store) Diff(path, from, to string, includePatch bool, maxPatchBytes int) ([]FileStat, string, bool, error) {
	repository, err := s.open(path)
	if err != nil {
		return nil, "", false, err
	}
	fromHash, err := resolve(repository, from)
	if err != nil {
		return nil, "", false, err
	}
	toHash, err := resolve(repository, to)
	if err != nil {
		return nil, "", false, err
	}
	fromCommit, err := repository.CommitObject(fromHash)
	if err != nil {
		return nil, "", false, fmt.Errorf("read commit %s: %w", fromHash, err)
	}
	toCommit, err := repository.CommitObject(toHash)
	if err != nil {
		return nil, "", false, fmt.Errorf("read commit %s: %w", toHash, err)
	}
	return diffCommits(fromCommit, toCommit, includePatch, maxPatchBytes)
}

// Blame attributes each line of a file at a revision.
func (s *Store) Blame(path, rev, file string, start, end int) ([]BlameLine, int, error) {
	repository, err := s.open(path)
	if err != nil {
		return nil, 0, err
	}
	hash, err := resolve(repository, rev)
	if err != nil {
		return nil, 0, err
	}
	commit, err := repository.CommitObject(hash)
	if err != nil {
		return nil, 0, fmt.Errorf("read commit %s: %w", hash, err)
	}
	result, err := git.Blame(commit, file)
	if err != nil {
		return nil, 0, fmt.Errorf("blame %s at %s: %w", file, rev, err)
	}

	total := len(result.Lines)
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > total {
		end = total
	}
	if start > total {
		return nil, total, nil
	}

	lines := make([]BlameLine, 0, end-start+1)
	for i := start - 1; i < end; i++ {
		line := result.Lines[i]
		if line == nil {
			continue
		}
		lines = append(lines, BlameLine{
			Line:   i + 1,
			Hash:   short(line.Hash.String()),
			Author: line.Author,
			When:   line.Date,
			Text:   line.Text,
		})
	}
	return lines, total, nil
}

// File returns a file's content at a revision. Unlike an index lookup this
// reaches any revision the clone holds, including tags the index never covered.
func (s *Store) File(path, rev, file string, maxBytes int) (string, bool, error) {
	repository, err := s.open(path)
	if err != nil {
		return "", false, err
	}
	hash, err := resolve(repository, rev)
	if err != nil {
		return "", false, err
	}
	commit, err := repository.CommitObject(hash)
	if err != nil {
		return "", false, fmt.Errorf("read commit %s: %w", hash, err)
	}
	entry, err := commit.File(file)
	if err != nil {
		return "", false, fmt.Errorf("read %s at %s: %w", file, describeRev(rev), err)
	}
	content, err := entry.Contents()
	if err != nil {
		return "", false, fmt.Errorf("read contents of %s: %w", file, err)
	}
	if maxBytes > 0 && len(content) > maxBytes {
		return content[:maxBytes], true, nil
	}
	return content, false, nil
}

func describeRev(rev string) string {
	if strings.TrimSpace(rev) == "" {
		return "the default branch"
	}
	return rev
}

// Refs lists the branches and tags the clone holds.
func (s *Store) Refs(path string) ([]Ref, error) {
	repository, err := s.open(path)
	if err != nil {
		return nil, err
	}
	iter, err := repository.References()
	if err != nil {
		return nil, fmt.Errorf("read refs: %w", err)
	}
	defer iter.Close()

	refs := make([]Ref, 0, 16)
	err = iter.ForEach(func(reference *plumbing.Reference) error {
		name := reference.Name()
		var kind string
		switch {
		case name.IsBranch():
			kind = "branch"
		case name.IsTag():
			kind = "tag"
		default:
			return nil
		}
		entry := Ref{Name: name.Short(), Kind: kind, Hash: short(reference.Hash().String())}
		// An annotated tag resolves through a tag object; a lightweight one
		// points straight at the commit. Either way the date is best effort.
		if commit, err := repository.CommitObject(reference.Hash()); err == nil {
			entry.When = commit.Author.When
		} else if tag, err := repository.TagObject(reference.Hash()); err == nil {
			if commit, err := tag.Commit(); err == nil {
				entry.When = commit.Author.When
			}
		}
		refs = append(refs, entry)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk refs: %w", err)
	}
	return refs, nil
}

// storeStopIteration ends a walk early without turning it into an error.
var storeStopIteration = fmt.Errorf("stop iteration")

// resolve turns a branch, tag, SHA or empty string into a commit hash. An empty
// revision means HEAD, which in a bare clone is the default branch.
func resolve(repository *git.Repository, rev string) (plumbing.Hash, error) {
	trimmed := strings.TrimSpace(rev)
	if trimmed == "" {
		head, err := repository.Head()
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("resolve HEAD: %w", err)
		}
		return head.Hash(), nil
	}
	hash, err := repository.ResolveRevision(plumbing.Revision(trimmed))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolve revision %q: %w", rev, err)
	}
	return *hash, nil
}

func diffCommits(from, to *object.Commit, includePatch bool, maxPatchBytes int) ([]FileStat, string, bool, error) {
	var patch *object.Patch
	var err error
	if from == nil {
		var tree *object.Tree
		if tree, err = to.Tree(); err != nil {
			return nil, "", false, fmt.Errorf("read tree: %w", err)
		}
		if patch, err = (&object.Tree{}).Patch(tree); err != nil {
			return nil, "", false, fmt.Errorf("diff root commit: %w", err)
		}
	} else if patch, err = from.Patch(to); err != nil {
		return nil, "", false, fmt.Errorf("diff %s..%s: %w", short(from.Hash.String()), short(to.Hash.String()), err)
	}

	stats := make([]FileStat, 0, len(patch.Stats()))
	for _, stat := range patch.Stats() {
		stats = append(stats, FileStat{Path: stat.Name, Additions: stat.Addition, Deletions: stat.Deletion})
	}
	if !includePatch {
		return stats, "", false, nil
	}

	text := patch.String()
	truncated := false
	if maxPatchBytes > 0 && len(text) > maxPatchBytes {
		text = text[:maxPatchBytes]
		truncated = true
	}
	return stats, text, truncated, nil
}

func convertCommit(commit *object.Commit) Commit {
	subject, body := splitMessage(commit.Message)
	converted := Commit{
		Hash:    commit.Hash.String(),
		Short:   short(commit.Hash.String()),
		Author:  commit.Author.Name,
		Email:   commit.Author.Email,
		When:    commit.Author.When,
		Subject: subject,
		Body:    body,
	}
	for _, parent := range commit.ParentHashes {
		converted.Parents = append(converted.Parents, short(parent.String()))
	}
	return converted
}

func splitMessage(message string) (string, string) {
	trimmed := strings.TrimRight(message, "\n")
	if index := strings.Index(trimmed, "\n"); index >= 0 {
		return trimmed[:index], strings.TrimLeft(trimmed[index+1:], "\n")
	}
	return trimmed, ""
}

func short(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}
