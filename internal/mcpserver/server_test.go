package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mappedsky/zoekt-mcp/internal/zoekt"
)

type fakeSearcher struct {
	lastQuery string
	lastOpts  *zoekt.SearchOptions
	result    *zoekt.SearchResult
	list      *zoekt.RepoList
	searches  int
}

func (f *fakeSearcher) Search(_ context.Context, query string, opts *zoekt.SearchOptions) (*zoekt.SearchResult, error) {
	f.searches++
	f.lastQuery = query
	f.lastOpts = opts
	if f.result == nil {
		return &zoekt.SearchResult{}, nil
	}
	return f.result, nil
}

func (f *fakeSearcher) List(_ context.Context, query string, _ *zoekt.ListOptions) (*zoekt.RepoList, error) {
	f.lastQuery = query
	if f.list == nil {
		return &zoekt.RepoList{}, nil
	}
	return f.list, nil
}

func testConfig() Config {
	config := DefaultConfig()
	config.SearchTimeout = time.Second
	return config
}

func TestSearchAppendsFiltersToTheCallerQuery(t *testing.T) {
	searcher := &fakeSearcher{}
	out, err := runSearchFor(t, searcher, SearchInput{
		Query:     "yaml.load",
		Repos:     []string{"github.com/example/repo"},
		Languages: []string{"python"},
		Branch:    "main",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	want := `yaml.load repo:^github\.com/example/repo$ lang:python branch:main`
	if searcher.lastQuery != want {
		t.Fatalf("query = %q; want %q", searcher.lastQuery, want)
	}
	if out.Query != want {
		t.Fatalf("reported query = %q; want %q", out.Query, want)
	}
	if !searcher.lastOpts.ChunkMatches {
		t.Fatal("ChunkMatches not requested")
	}
}

func TestSearchRendersSeveralReposAsADisjunction(t *testing.T) {
	searcher := &fakeSearcher{}
	if _, err := runSearchFor(t, searcher, SearchInput{
		Query: "TODO",
		Repos: []string{"a", "b"},
	}); err != nil {
		t.Fatalf("search: %v", err)
	}
	want := `TODO (repo:^a$ or repo:^b$)`
	if searcher.lastQuery != want {
		t.Fatalf("query = %q; want %q", searcher.lastQuery, want)
	}
}

func TestSearchRejectsAnEmptyQuery(t *testing.T) {
	if _, err := runSearchFor(t, &fakeSearcher{}, SearchInput{Query: "  "}); err == nil {
		t.Fatal("empty query accepted; want an error")
	}
}

func TestSearchCapsFilesAndChunks(t *testing.T) {
	chunks := make([]zoekt.ChunkMatch, 4)
	for i := range chunks {
		chunks[i] = zoekt.ChunkMatch{Content: []byte("x\n"), ContentStart: zoekt.Location{LineNumber: uint32(i + 1)}}
	}
	searcher := &fakeSearcher{result: &zoekt.SearchResult{
		Files: []zoekt.FileMatch{{FileName: "a.go", Repository: "repo", ChunkMatches: chunks}},
		Stats: zoekt.Stats{MatchCount: 4},
	}}

	config := testConfig()
	config.MaxChunksPerFile = 2
	out, err := runSearch(context.Background(), searcher, config, "x", 5, 0, "")
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if searcher.lastOpts.MaxDocDisplayCount != 5 {
		t.Fatalf("MaxDocDisplayCount = %d; want 5", searcher.lastOpts.MaxDocDisplayCount)
	}
	file := out.Files[0]
	if len(file.Chunks) != 2 {
		t.Fatalf("chunks = %d; want 2", len(file.Chunks))
	}
	if file.Elided != 2 {
		t.Fatalf("elided = %d; want 2", file.Elided)
	}
}

func TestSearchRequestOverAServerCeilingIsClamped(t *testing.T) {
	searcher := &fakeSearcher{}
	config := testConfig()
	config.MaxFiles = 10
	if _, err := runSearch(context.Background(), searcher, config, "x", 1000, 0, ""); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if searcher.lastOpts.MaxDocDisplayCount != 10 {
		t.Fatalf("MaxDocDisplayCount = %d; want the ceiling of 10", searcher.lastOpts.MaxDocDisplayCount)
	}
}

func TestConvertChunkReportsMatchLinesAndSymbols(t *testing.T) {
	chunk := convertChunk(zoekt.ChunkMatch{
		Content:      []byte("line one\nline two\n"),
		ContentStart: zoekt.Location{LineNumber: 10},
		Ranges: []zoekt.Range{
			{Start: zoekt.Location{LineNumber: 11}},
			{Start: zoekt.Location{LineNumber: 11}},
		},
		SymbolInfo: []*zoekt.Symbol{
			{Sym: "Handle", Kind: "function", Parent: "Server"},
			nil,
		},
	})
	if chunk.StartLine != 10 {
		t.Fatalf("start line = %d; want 10", chunk.StartLine)
	}
	if len(chunk.MatchLines) != 1 || chunk.MatchLines[0] != 11 {
		t.Fatalf("match lines = %v; want [11] with duplicates collapsed", chunk.MatchLines)
	}
	if len(chunk.Symbols) != 1 || chunk.Symbols[0] != "Server.Handle (function)" {
		t.Fatalf("symbols = %v; want [Server.Handle (function)]", chunk.Symbols)
	}
}

func TestFindReferencesBuildsAWholeWordCaseSensitiveQuery(t *testing.T) {
	searcher := &fakeSearcher{}
	query := zoekt.Join(zoekt.WordRegexp("load"), "case:yes", filters(nil, nil, ""))
	if _, err := runSearch(context.Background(), searcher, testConfig(), query, 0, 0, ""); err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	want := `\bload\b case:yes`
	if searcher.lastQuery != want {
		t.Fatalf("query = %q; want %q", searcher.lastQuery, want)
	}
}

func TestGetFileAnchorsRepoAndPathAndTruncates(t *testing.T) {
	searcher := &fakeSearcher{result: &zoekt.SearchResult{
		Files: []zoekt.FileMatch{{
			FileName:   "cmd/main.go",
			Repository: "github.com/example/repo",
			Content:    []byte("package main\nfunc main() {}\n"),
		}},
	}}
	config := testConfig()
	config.MaxFileBytes = 12

	out, err := getFile(context.Background(), searcher, config, FileInput{
		Repo:   "github.com/example/repo",
		Path:   "cmd/main.go",
		Branch: "main",
	})
	if err != nil {
		t.Fatalf("getFile: %v", err)
	}

	want := `repo:^github\.com/example/repo$ file:^cmd/main\.go$ branch:main`
	if searcher.lastQuery != want {
		t.Fatalf("query = %q; want %q", searcher.lastQuery, want)
	}
	if !searcher.lastOpts.Whole {
		t.Fatal("Whole not requested; the file body would be empty")
	}
	if out.Content != "package main" {
		t.Fatalf("content = %q; want it truncated to the byte limit", out.Content)
	}
	if !out.Truncated {
		t.Fatal("truncated flag not set")
	}
	if out.Lines != 1 {
		t.Fatalf("lines = %d; want 1", out.Lines)
	}
}

func TestGetFileReportsAMissingFileClearly(t *testing.T) {
	_, err := getFile(context.Background(), &fakeSearcher{}, testConfig(), FileInput{Repo: "repo", Path: "nope.go"})
	if err == nil {
		t.Fatal("missing file accepted; want an error")
	}
	if !strings.Contains(err.Error(), "zoekt_list_repos") {
		t.Fatalf("error = %v; want it to point at zoekt_list_repos", err)
	}
}

func TestListReposMatchesEverythingByDefault(t *testing.T) {
	indexed := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	searcher := &fakeSearcher{list: &zoekt.RepoList{Repos: []*zoekt.RepoListEntry{
		{
			Repository: zoekt.Repository{
				Name:       "github.com/example/repo",
				HasSymbols: true,
				Branches:   []zoekt.RepositoryBranch{{Name: "main"}, {Name: "release"}},
			},
			IndexMetadata: zoekt.IndexMetadata{IndexTime: indexed},
			Stats:         zoekt.RepoStats{Documents: 42},
		},
		nil,
	}}}

	out, err := listRepos(context.Background(), searcher, ReposInput{})
	if err != nil {
		t.Fatalf("listRepos: %v", err)
	}
	if searcher.lastQuery != zoekt.MatchAllRepos {
		t.Fatalf("query = %q; want %q", searcher.lastQuery, zoekt.MatchAllRepos)
	}
	if out.Count != 1 {
		t.Fatalf("count = %d; want 1 with the nil entry skipped", out.Count)
	}
	repo := out.Repos[0]
	if len(repo.Branches) != 2 || repo.Branches[0] != "main" {
		t.Fatalf("branches = %v; want [main release]", repo.Branches)
	}
	if repo.IndexedAt != "2026-09-01T12:00:00Z" {
		t.Fatalf("indexed at = %q; want RFC 3339", repo.IndexedAt)
	}
}

func TestListReposAppliesAFilter(t *testing.T) {
	searcher := &fakeSearcher{}
	if _, err := listRepos(context.Background(), searcher, ReposInput{Filter: "^github\\.com/example/"}); err != nil {
		t.Fatalf("listRepos: %v", err)
	}
	if searcher.lastQuery != `r:^github\.com/example/` {
		t.Fatalf("query = %q; want the filter as a repo atom", searcher.lastQuery)
	}
}

// runSearchFor mirrors what the zoekt_search_code handler does, so the query
// assembly is covered without going through the MCP transport.
func runSearchFor(t *testing.T, searcher Searcher, in SearchInput) (SearchOutput, error) {
	t.Helper()
	if err := require("query", in.Query); err != nil {
		return SearchOutput{}, err
	}
	query := zoekt.Join(in.Query, filters(in.Repos, in.Languages, in.Branch))
	return runSearch(context.Background(), searcher, testConfig(), query, in.MaxFiles, in.ContextLines, "")
}

// zoekt resolves lang: through a name/alias table and compiles a miss to a
// constant false, so anchoring or escaping the value would turn a working
// filter into a query that silently matches nothing.
func TestLanguageFilterIsAPlainNameNotARegexp(t *testing.T) {
	got := filters(nil, []string{"python"}, "")
	if got != "lang:python" {
		t.Fatalf("filters = %q; want lang:python", got)
	}
}

// branch: is a substring test against the branch name, so an anchored regexp
// would look for that literal text inside the name and never match.
func TestBranchFilterIsAPlainNameNotARegexp(t *testing.T) {
	got := filters(nil, nil, "main")
	if got != "branch:main" {
		t.Fatalf("filters = %q; want branch:main", got)
	}
}

func TestFiltersQuoteValuesCarryingWhitespace(t *testing.T) {
	got := filters(nil, []string{"Emacs Lisp"}, "")
	if got != `lang:"Emacs Lisp"` {
		t.Fatalf("filters = %q; want the value quoted", got)
	}
}

// zoekt embeds Stats in SearchResult, so the counters arrive flattened rather
// than nested. A named field would decode every one of them as zero.
func TestSearchReadsFlattenedStats(t *testing.T) {
	searcher := &fakeSearcher{result: &zoekt.SearchResult{
		Stats: zoekt.Stats{MatchCount: 17, FilesSkipped: 3},
		Files: []zoekt.FileMatch{{FileName: "a.go", Repository: "repo"}},
	}}
	out, err := runSearch(context.Background(), searcher, testConfig(), "x", 5, 0, "")
	if err != nil {
		t.Fatalf("runSearch: %v", err)
	}
	if out.MatchCount != 17 {
		t.Fatalf("match count = %d; want 17", out.MatchCount)
	}
	if !out.Truncated {
		t.Fatal("skipped files did not mark the result truncated")
	}
}

// A filename match puts the path in Content with FileName set. Rendering it as
// a chunk would claim line 1 of the file holds its own name.
func TestFilenameMatchIsNotRenderedAsContent(t *testing.T) {
	hits := convertFile(zoekt.FileMatch{
		FileName:   "go.mod",
		Repository: "repo",
		ChunkMatches: []zoekt.ChunkMatch{{
			Content:      []byte("go.mod"),
			ContentStart: zoekt.Location{LineNumber: 1},
			FileName:     true,
		}},
	}, 10)
	if len(hits.Chunks) != 0 {
		t.Fatalf("chunks = %+v; want none for a path-only match", hits.Chunks)
	}
	if !hits.PathMatch {
		t.Fatal("path_match not set")
	}
}

func TestListFilesSearchesPathsOnly(t *testing.T) {
	searcher := &fakeSearcher{result: &zoekt.SearchResult{
		Files: []zoekt.FileMatch{{FileName: "requirements.txt", Repository: "seizu", Language: "Text"}},
	}}
	out, err := listFiles(context.Background(), searcher, testConfig(), FilesInput{
		Pattern: `requirements\.txt$`,
		Repos:   []string{"seizu"},
	})
	if err != nil {
		t.Fatalf("listFiles: %v", err)
	}
	want := `file:requirements\.txt$ repo:^seizu$`
	if searcher.lastQuery != want {
		t.Fatalf("query = %q; want %q", searcher.lastQuery, want)
	}
	if searcher.lastOpts.ChunkMatches {
		t.Fatal("path listing asked for content chunks")
	}
	if out.Count != 1 || out.Files[0].Path != "requirements.txt" {
		t.Fatalf("files = %+v; want the one path", out.Files)
	}
}

func TestListReposContainingNarrowsByContent(t *testing.T) {
	searcher := &fakeSearcher{}
	if _, err := listRepos(context.Background(), searcher, ReposInput{Containing: "yaml.safe_load"}); err != nil {
		t.Fatalf("listRepos: %v", err)
	}
	if searcher.lastQuery != "yaml.safe_load" {
		t.Fatalf("query = %q; want the content query", searcher.lastQuery)
	}
}

func TestListReposCombinesContainingWithFilter(t *testing.T) {
	searcher := &fakeSearcher{}
	if _, err := listRepos(context.Background(), searcher, ReposInput{
		Containing: "yaml.safe_load",
		Filter:     "^seizu$",
	}); err != nil {
		t.Fatalf("listRepos: %v", err)
	}
	if searcher.lastQuery != `yaml.safe_load r:^seizu$` {
		t.Fatalf("query = %q; want both atoms", searcher.lastQuery)
	}
}
