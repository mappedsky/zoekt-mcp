// Package mcpserver exposes a zoekt index as Model Context Protocol tools.
package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mappedsky/zoekt-mcp/internal/gitrepo"
	"github.com/mappedsky/zoekt-mcp/internal/zoekt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	Name    = "zoekt-mcp"
	Version = "0.2.0"
)

// Config bounds every response so one tool call cannot exhaust a model turn.
type Config struct {
	// MaxFiles caps files per search when a call does not ask for fewer.
	MaxFiles int
	// MaxChunksPerFile caps the match chunks reported for a single file.
	MaxChunksPerFile int
	// ContextLines pads each chunk when a call does not ask for its own.
	ContextLines int
	// MaxFileBytes caps the content returned by zoekt_get_file and git_file.
	MaxFileBytes int
	// SearchTimeout bounds the search server-side.
	SearchTimeout time.Duration
	// MaxCommits caps one history walk.
	MaxCommits int
	// MaxPatchBytes caps a returned unified diff.
	MaxPatchBytes int
	// MaxBlameLines caps one blame range.
	MaxBlameLines int
	// RepoPathTTL is how long a name-to-clone mapping is reused. Clone
	// directories are stable across re-indexes, so this can be generous.
	RepoPathTTL time.Duration
}

// DefaultConfig returns bounds sized for a chat turn rather than a bulk export.
func DefaultConfig() Config {
	return Config{
		MaxFiles:         30,
		MaxChunksPerFile: 10,
		ContextLines:     2,
		MaxFileBytes:     256 * 1024,
		SearchTimeout:    20 * time.Second,
		MaxCommits:       50,
		MaxPatchBytes:    128 * 1024,
		MaxBlameLines:    2000,
		RepoPathTTL:      10 * time.Minute,
	}
}

// Searcher is the zoekt surface the tools depend on.
type Searcher interface {
	Search(ctx context.Context, query string, opts *zoekt.SearchOptions) (*zoekt.SearchResult, error)
	List(ctx context.Context, query string, opts *zoekt.ListOptions) (*zoekt.RepoList, error)
}

// SearchInput is the general-purpose code search request.
type SearchInput struct {
	Query        string   `json:"query" jsonschema:"Zoekt query. Bare terms are regular expressions over file content; add atoms to narrow, for example 'repo:^github\\.com/org/name$', 'file:\\.go$', 'lang:python', 'sym:ParseConfig', 'case:yes', or '-file:_test\\.go' to exclude."`
	Repos        []string `json:"repos,omitempty" jsonschema:"Restrict to these exact repository names, as reported by zoekt_list_repos."`
	Languages    []string `json:"languages,omitempty" jsonschema:"Restrict to these languages by name or alias, for example python or go. An unrecognized name matches nothing."`
	Branch       string   `json:"branch,omitempty" jsonschema:"Restrict to an indexed branch, matched as a substring of the branch name. Defaults to every branch the index holds."`
	MaxFiles     int      `json:"max_files,omitempty" jsonschema:"Maximum files to return. Defaults to the server limit."`
	ContextLines int      `json:"context_lines,omitempty" jsonschema:"Lines of context around each match. Defaults to the server setting."`
}

// SymbolInput looks up definitions by name.
type SymbolInput struct {
	Symbol    string   `json:"symbol" jsonschema:"Symbol name to find definitions for, for example ParseConfig."`
	Repos     []string `json:"repos,omitempty" jsonschema:"Restrict to these exact repository names."`
	Languages []string `json:"languages,omitempty" jsonschema:"Restrict to these languages by name or alias. An unrecognized name matches nothing."`
	Branch    string   `json:"branch,omitempty" jsonschema:"Restrict to an indexed branch, matched as a substring of the branch name."`
	MaxFiles  int      `json:"max_files,omitempty" jsonschema:"Maximum files to return."`
}

// ReferencesInput finds every mention of a name.
type ReferencesInput struct {
	Symbol       string   `json:"symbol" jsonschema:"Identifier to find mentions of. Matched as a whole word, case-sensitively."`
	Repos        []string `json:"repos,omitempty" jsonschema:"Restrict to these exact repository names."`
	Languages    []string `json:"languages,omitempty" jsonschema:"Restrict to these languages by name or alias. An unrecognized name matches nothing."`
	Branch       string   `json:"branch,omitempty" jsonschema:"Restrict to an indexed branch, matched as a substring of the branch name."`
	MaxFiles     int      `json:"max_files,omitempty" jsonschema:"Maximum files to return."`
	ContextLines int      `json:"context_lines,omitempty" jsonschema:"Lines of context around each mention."`
}

// FileInput reads one indexed file.
type FileInput struct {
	Repo   string `json:"repo" jsonschema:"Exact repository name, as reported by zoekt_list_repos."`
	Path   string `json:"path" jsonschema:"Repository-relative file path."`
	Branch string `json:"branch,omitempty" jsonschema:"Indexed branch to read, matched as a substring of the branch name. Defaults to whichever branch the index returns first."`
}

// FilesInput searches file paths rather than file contents.
type FilesInput struct {
	Pattern  string   `json:"pattern" jsonschema:"Regular expression matched against repository-relative file paths, for example 'requirements\\.txt$' or '^src/.*\\.go$'."`
	Repos    []string `json:"repos,omitempty" jsonschema:"Restrict to these exact repository names."`
	Branch   string   `json:"branch,omitempty" jsonschema:"Restrict to an indexed branch, matched as a substring of the branch name."`
	MaxFiles int      `json:"max_files,omitempty" jsonschema:"Maximum paths to return."`
}

// FileEntry is one indexed path.
type FileEntry struct {
	Repo     string   `json:"repo" jsonschema:"Repository name."`
	Path     string   `json:"path" jsonschema:"Repository-relative file path."`
	Language string   `json:"language,omitempty" jsonschema:"Language zoekt detected for the file."`
	Branches []string `json:"branches,omitempty" jsonschema:"Indexed branches containing this file."`
}

// FilesOutput lists matching paths.
type FilesOutput struct {
	Query     string      `json:"query" jsonschema:"The zoekt query that was executed."`
	Files     []FileEntry `json:"files" jsonschema:"Matching file paths."`
	Count     int         `json:"count" jsonschema:"Number of paths returned."`
	Truncated bool        `json:"truncated" jsonschema:"Whether results were cut off by a limit."`
}

// ReposInput lists indexed repositories.
type ReposInput struct {
	Filter     string `json:"filter,omitempty" jsonschema:"Regular expression matched against repository names. Omit to list everything indexed."`
	Containing string `json:"containing,omitempty" jsonschema:"Zoekt query. Returns only repositories that could contain a match, without returning any file content."`
}

// Chunk is a contiguous run of lines containing at least one match.
type Chunk struct {
	StartLine  int      `json:"start_line" jsonschema:"1-based line number of the first line of content."`
	MatchLines []int    `json:"match_lines,omitempty" jsonschema:"Line numbers within this chunk that matched."`
	Symbols    []string `json:"symbols,omitempty" jsonschema:"Symbol definitions ctags recorded at the matched lines."`
	Content    string   `json:"content" jsonschema:"The chunk's lines, including any requested context."`
}

// FileHits is one file and the matches found in it.
type FileHits struct {
	Repo      string   `json:"repo" jsonschema:"Repository name."`
	Path      string   `json:"path" jsonschema:"Repository-relative file path."`
	Language  string   `json:"language,omitempty" jsonschema:"Language zoekt detected for the file."`
	Branches  []string `json:"branches,omitempty" jsonschema:"Indexed branches containing this version of the file."`
	Version   string   `json:"version,omitempty" jsonschema:"Commit the indexed content came from."`
	Chunks    []Chunk  `json:"chunks,omitempty" jsonschema:"Matching regions of the file."`
	PathMatch bool     `json:"path_match,omitempty" jsonschema:"Whether the file path itself matched, as opposed to its content."`
	Elided    int      `json:"elided_chunks,omitempty" jsonschema:"Matching chunks omitted from this file because of the per-file cap."`
}

// SearchOutput is the shared shape of every search-style tool.
type SearchOutput struct {
	Query      string     `json:"query" jsonschema:"The zoekt query that was executed, after filters were applied."`
	Files      []FileHits `json:"files" jsonschema:"Files containing matches."`
	FileCount  int        `json:"file_count" jsonschema:"Number of files returned."`
	MatchCount int        `json:"match_count" jsonschema:"Total matches zoekt reported for the query."`
	Truncated  bool       `json:"truncated" jsonschema:"Whether results were cut off by a limit; narrow the query to see the rest."`
	Note       string     `json:"note,omitempty" jsonschema:"How to read this result when that is not obvious."`
}

// FileOutput is one whole indexed file.
type FileOutput struct {
	Repo      string   `json:"repo" jsonschema:"Repository name."`
	Path      string   `json:"path" jsonschema:"Repository-relative file path."`
	Language  string   `json:"language,omitempty" jsonschema:"Language zoekt detected for the file."`
	Branches  []string `json:"branches,omitempty" jsonschema:"Indexed branches containing this version of the file."`
	Version   string   `json:"version,omitempty" jsonschema:"Commit the indexed content came from."`
	Lines     int      `json:"lines" jsonschema:"Number of lines returned."`
	Bytes     int      `json:"bytes" jsonschema:"Number of bytes returned."`
	Truncated bool     `json:"truncated,omitempty" jsonschema:"Whether the file was cut off at the server's byte limit."`
	Content   string   `json:"content" jsonschema:"File content as indexed."`
}

// RepoInfo is one indexed repository.
type RepoInfo struct {
	Name       string   `json:"name" jsonschema:"Repository name, as zoekt indexed it."`
	URL        string   `json:"url,omitempty" jsonschema:"Repository URL when the indexer recorded one."`
	Branches   []string `json:"branches,omitempty" jsonschema:"Indexed branches."`
	HasSymbols bool     `json:"has_symbols" jsonschema:"Whether ctags symbol data is present, which zoekt_search_symbols requires."`
	Documents  int      `json:"documents,omitempty" jsonschema:"Number of indexed files."`
	IndexedAt  string   `json:"indexed_at,omitempty" jsonschema:"When the shard was built, RFC 3339."`
}

// ReposOutput lists what the index holds.
type ReposOutput struct {
	Query string     `json:"query" jsonschema:"The zoekt query that was executed."`
	Repos []RepoInfo `json:"repos" jsonschema:"Indexed repositories."`
	Count int        `json:"count" jsonschema:"Number of repositories returned."`
}

// New creates an MCP server exposing search over the zoekt index behind client.
//
// When store is non-nil the history tools are registered as well, reading the
// same clones the index was built from. Passing nil leaves the server
// search-only, which is what a deployment without access to that volume wants.
func New(client Searcher, store *gitrepo.Store, config Config) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:        Name,
		Title:       "zoekt code search",
		Description: "Search, read and trace the history of source code across the repositories indexed by zoekt.",
		Version:     Version,
		WebsiteURL:  "https://github.com/mappedsky/zoekt-mcp",
	}, nil)

	mcp.AddTool(server, tool(
		"zoekt_search_code",
		"Search file contents across every indexed repository. Bare terms are regular expressions; narrow with repo, file, lang, sym, and case atoms. Use this first to locate code, then zoekt_get_file to read a hit in full.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
		if err := require("query", in.Query); err != nil {
			return nil, SearchOutput{}, err
		}
		query := zoekt.Join(in.Query, filters(in.Repos, in.Languages, in.Branch))
		out, err := runSearch(ctx, client, config, query, in.MaxFiles, in.ContextLines, "")
		return nil, out, err
	})

	mcp.AddTool(server, tool(
		"zoekt_search_symbols",
		"Find where a symbol is defined, using the ctags data built at index time. Narrower and more precise than zoekt_find_references, which also returns call sites and mentions in comments.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in SymbolInput) (*mcp.CallToolResult, SearchOutput, error) {
		if err := requireIdentifier("symbol", in.Symbol); err != nil {
			return nil, SearchOutput{}, err
		}
		query := zoekt.Join(
			zoekt.Atom("sym", zoekt.WordRegexp(in.Symbol)),
			filters(in.Repos, in.Languages, in.Branch),
		)
		out, err := runSearch(ctx, client, config, query, in.MaxFiles, 0,
			"Definitions only. A repository whose has_symbols is false contributes nothing here.")
		return nil, out, err
	})

	mcp.AddTool(server, tool(
		"zoekt_find_references",
		"Find every whole-word, case-sensitive mention of an identifier across the index: definitions, call sites, imports, strings, and comments alike. This is a textual search, not a resolved reference set, so read the cited lines before drawing a conclusion.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in ReferencesInput) (*mcp.CallToolResult, SearchOutput, error) {
		if err := requireIdentifier("symbol", in.Symbol); err != nil {
			return nil, SearchOutput{}, err
		}
		query := zoekt.Join(
			zoekt.WordRegexp(in.Symbol),
			"case:yes",
			filters(in.Repos, in.Languages, in.Branch),
		)
		out, err := runSearch(ctx, client, config, query, in.MaxFiles, in.ContextLines,
			"Textual whole-word matches, including comments and strings. Not a resolved reference set.")
		return nil, out, err
	})

	mcp.AddTool(server, tool(
		"zoekt_get_file",
		"Read one indexed file in full. The content is what zoekt indexed at its last sync, which can lag the repository.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in FileInput) (*mcp.CallToolResult, FileOutput, error) {
		if err := require("repo", in.Repo); err != nil {
			return nil, FileOutput{}, err
		}
		if err := require("path", in.Path); err != nil {
			return nil, FileOutput{}, err
		}
		out, err := getFile(ctx, client, config, in)
		return nil, out, err
	})

	mcp.AddTool(server, tool(
		"zoekt_list_files",
		"List indexed file paths matching a pattern, without reading any content. Use it to check whether a manifest or lockfile exists across the organization, or to see a repository's layout before reading anything.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in FilesInput) (*mcp.CallToolResult, FilesOutput, error) {
		if err := require("pattern", in.Pattern); err != nil {
			return nil, FilesOutput{}, err
		}
		out, err := listFiles(ctx, client, config, in)
		return nil, out, err
	})

	mcp.AddTool(server, tool(
		"zoekt_list_repos",
		"List the repositories in the index, with their branches and when each was last indexed. Call this to learn the exact repository names the other tools expect. With `containing`, it narrows to the repositories that could match a query and returns no file content, which is the cheap way to scope a search across the whole organization before reading anything.",
	), func(ctx context.Context, _ *mcp.CallToolRequest, in ReposInput) (*mcp.CallToolResult, ReposOutput, error) {
		out, err := listRepos(ctx, client, in)
		return nil, out, err
	})

	addGitTools(server, store, newRepoLocator(client, config.RepoPathTTL), config)

	return server
}

func runSearch(ctx context.Context, client Searcher, config Config, query string, maxFiles, contextLines int, note string) (SearchOutput, error) {
	files := clamp(maxFiles, config.MaxFiles)
	lines := clamp(contextLines, config.ContextLines)

	result, err := client.Search(ctx, query, &zoekt.SearchOptions{
		ChunkMatches:       true,
		NumContextLines:    lines,
		MaxDocDisplayCount: files,
		MaxWallTime:        config.SearchTimeout,
	})
	if err != nil {
		return SearchOutput{}, err
	}

	out := SearchOutput{
		Query:      query,
		Files:      make([]FileHits, 0, len(result.Files)),
		MatchCount: result.MatchCount,
		Note:       note,
	}
	for _, file := range result.Files {
		out.Files = append(out.Files, convertFile(file, config.MaxChunksPerFile))
	}
	out.FileCount = len(out.Files)
	out.Truncated = len(result.Files) >= files || result.FilesSkipped > 0
	return out, nil
}

func convertFile(file zoekt.FileMatch, maxChunks int) FileHits {
	hits := FileHits{
		Repo:     file.Repository,
		Path:     file.FileName,
		Language: file.Language,
		Branches: file.Branches,
		Version:  file.Version,
	}
	// A filename match carries the path in Content with FileName set, which
	// would otherwise render as a content chunk claiming line 1 of the file
	// holds its own name. Report it as what it is and keep it out of Chunks.
	chunks := make([]zoekt.ChunkMatch, 0, len(file.ChunkMatches))
	for _, chunk := range file.ChunkMatches {
		if chunk.FileName {
			hits.PathMatch = true
			continue
		}
		chunks = append(chunks, chunk)
	}
	if maxChunks > 0 && len(chunks) > maxChunks {
		hits.Elided = len(chunks) - maxChunks
		chunks = chunks[:maxChunks]
	}
	for _, chunk := range chunks {
		hits.Chunks = append(hits.Chunks, convertChunk(chunk))
	}
	return hits
}

func convertChunk(chunk zoekt.ChunkMatch) Chunk {
	converted := Chunk{
		StartLine: int(chunk.ContentStart.LineNumber),
		Content:   string(chunk.Content),
	}
	seenLine := make(map[int]bool, len(chunk.Ranges))
	for _, span := range chunk.Ranges {
		line := int(span.Start.LineNumber)
		if !seenLine[line] {
			seenLine[line] = true
			converted.MatchLines = append(converted.MatchLines, line)
		}
	}
	seenSymbol := make(map[string]bool, len(chunk.SymbolInfo))
	for _, symbol := range chunk.SymbolInfo {
		rendered := renderSymbol(symbol)
		if rendered != "" && !seenSymbol[rendered] {
			seenSymbol[rendered] = true
			converted.Symbols = append(converted.Symbols, rendered)
		}
	}
	return converted
}

func renderSymbol(symbol *zoekt.Symbol) string {
	if symbol == nil || symbol.Sym == "" {
		return ""
	}
	name := symbol.Sym
	if symbol.Parent != "" {
		name = symbol.Parent + "." + name
	}
	if symbol.Kind == "" {
		return name
	}
	return name + " (" + symbol.Kind + ")"
}

func getFile(ctx context.Context, client Searcher, config Config, in FileInput) (FileOutput, error) {
	query := zoekt.Join(
		zoekt.ExactAtom("repo", in.Repo),
		zoekt.ExactAtom("file", in.Path),
		branchAtom(in.Branch),
	)
	result, err := client.Search(ctx, query, &zoekt.SearchOptions{
		Whole:              true,
		MaxDocDisplayCount: 1,
		MaxWallTime:        config.SearchTimeout,
	})
	if err != nil {
		return FileOutput{}, err
	}
	if len(result.Files) == 0 {
		return FileOutput{}, fmt.Errorf("no indexed file %q in repository %q; check zoekt_list_repos for the exact repository name", in.Path, in.Repo)
	}

	file := result.Files[0]
	content := file.Content
	out := FileOutput{
		Repo:     file.Repository,
		Path:     file.FileName,
		Language: file.Language,
		Branches: file.Branches,
		Version:  file.Version,
	}
	if config.MaxFileBytes > 0 && len(content) > config.MaxFileBytes {
		content = content[:config.MaxFileBytes]
		out.Truncated = true
	}
	out.Content = string(content)
	out.Bytes = len(content)
	out.Lines = countLines(out.Content)
	return out, nil
}

func listFiles(ctx context.Context, client Searcher, config Config, in FilesInput) (FilesOutput, error) {
	files := clamp(in.MaxFiles, config.MaxFiles)
	query := zoekt.Join(
		zoekt.Atom("file", in.Pattern),
		anyOf("repo", in.Repos, zoekt.ExactAtom),
		branchAtom(in.Branch),
	)
	result, err := client.Search(ctx, query, &zoekt.SearchOptions{
		MaxDocDisplayCount: files,
		MaxWallTime:        config.SearchTimeout,
	})
	if err != nil {
		return FilesOutput{}, err
	}

	out := FilesOutput{Query: query, Files: make([]FileEntry, 0, len(result.Files))}
	for _, file := range result.Files {
		out.Files = append(out.Files, FileEntry{
			Repo:     file.Repository,
			Path:     file.FileName,
			Language: file.Language,
			Branches: file.Branches,
		})
	}
	out.Count = len(out.Files)
	out.Truncated = len(result.Files) >= files
	return out, nil
}

func listRepos(ctx context.Context, client Searcher, in ReposInput) (ReposOutput, error) {
	// zoekt rejects an empty query, so listing everything needs an atom that
	// always matches. A content query narrows the listing to the shards that
	// could match it, which is what makes this a cheap organization-wide sweep.
	query := zoekt.Join(strings.TrimSpace(in.Containing))
	if strings.TrimSpace(in.Filter) != "" {
		query = zoekt.Join(query, zoekt.Atom("r", in.Filter))
	}
	if query == "" {
		query = zoekt.MatchAllRepos
	}
	list, err := client.List(ctx, query, &zoekt.ListOptions{Field: zoekt.RepoListFieldRepos})
	if err != nil {
		return ReposOutput{}, err
	}

	out := ReposOutput{Query: query, Repos: make([]RepoInfo, 0, len(list.Repos))}
	for _, entry := range list.Repos {
		if entry == nil {
			continue
		}
		info := RepoInfo{
			Name:       entry.Repository.Name,
			URL:        entry.Repository.URL,
			HasSymbols: entry.Repository.HasSymbols,
			Documents:  entry.Stats.Documents,
		}
		for _, branch := range entry.Repository.Branches {
			info.Branches = append(info.Branches, branch.Name)
		}
		if !entry.IndexMetadata.IndexTime.IsZero() {
			info.IndexedAt = entry.IndexMetadata.IndexTime.UTC().Format(time.RFC3339)
		}
		out.Repos = append(out.Repos, info)
	}
	out.Count = len(out.Repos)
	return out, nil
}

// filters renders the optional narrowing atoms shared by the search tools.
func filters(repos, languages []string, branch string) string {
	atoms := make([]string, 0, len(repos)+len(languages)+1)
	if repoAtom := anyOf("repo", repos, zoekt.ExactAtom); repoAtom != "" {
		atoms = append(atoms, repoAtom)
	}
	if languageAtom := anyOf("lang", languages, zoekt.PlainAtom); languageAtom != "" {
		atoms = append(atoms, languageAtom)
	}
	atoms = append(atoms, branchAtom(branch))
	return zoekt.Join(atoms...)
}

// anyOf renders a disjunction over values, parenthesized so it binds ahead of
// the surrounding conjunction. render decides how each value is escaped, which
// differs per prefix: see zoekt.ExactAtom and zoekt.PlainAtom.
func anyOf(prefix string, values []string, render func(string, string) string) string {
	kept := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			kept = append(kept, render(prefix, trimmed))
		}
	}
	switch len(kept) {
	case 0:
		return ""
	case 1:
		return kept[0]
	default:
		return "(" + strings.Join(kept, " or ") + ")"
	}
}

func branchAtom(branch string) string {
	if strings.TrimSpace(branch) == "" {
		return ""
	}
	// branch is a substring test in zoekt, so this also admits a branch whose
	// name contains the requested one.
	return zoekt.PlainAtom("branch", strings.TrimSpace(branch))
}

func countLines(content string) int {
	if content == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(content, "\n"), "\n") + 1
}

// clamp keeps a caller's request within the configured ceiling, falling back to
// the ceiling when the caller did not ask for anything.
func clamp(requested, ceiling int) int {
	if requested <= 0 || requested > ceiling {
		return ceiling
	}
	return requested
}

func tool(name, description string) *mcp.Tool {
	destructive, openWorld := false, false
	return &mcp.Tool{
		Name:        name,
		Description: description,
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: &destructive,
			IdempotentHint:  true,
			// The index is a fixed internal corpus, not the open web.
			OpenWorldHint: &openWorld,
			ReadOnlyHint:  true,
		},
	}
}

func require(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s must not be empty", field)
	}
	return nil
}

// requireIdentifier rejects values the zoekt tokenizer would split or reinterpret.
func requireIdentifier(field, value string) error {
	if err := require(field, value); err != nil {
		return err
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return fmt.Errorf("%s must be a single identifier without whitespace, got %q", field, value)
	}
	return nil
}
