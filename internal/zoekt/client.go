// Package zoekt is a minimal client for the zoekt-webserver JSON API.
//
// It deliberately models the wire format rather than importing
// github.com/sourcegraph/zoekt: the Go API carries a large dependency tree and
// changes more often than the JSON, and the index this talks to may be served
// by a fork (Sourcebot vendors its own) whose Go types we do not want to pin.
package zoekt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxErrorBody bounds how much of a failed response is quoted back in an error.
const maxErrorBody = 4096

// SearchOptions is the subset of zoekt.SearchOptions this client sets.
// Fields are omitted when zero so the server applies its own heuristics.
type SearchOptions struct {
	// Whole returns the complete file content in FileMatch.Content.
	Whole bool `json:"Whole,omitempty"`
	// ChunkMatches selects the ChunkMatches response shape over LineMatches.
	ChunkMatches bool `json:"ChunkMatches,omitempty"`
	// NumContextLines pads each chunk with surrounding lines.
	NumContextLines int `json:"NumContextLines,omitempty"`
	// MaxDocDisplayCount caps the number of files returned.
	MaxDocDisplayCount int `json:"MaxDocDisplayCount,omitempty"`
	// MaxMatchDisplayCount caps the number of matches returned.
	MaxMatchDisplayCount int `json:"MaxMatchDisplayCount,omitempty"`
	// MaxWallTime bounds the search server-side. time.Duration marshals as
	// integer nanoseconds, which is what zoekt expects.
	MaxWallTime time.Duration `json:"MaxWallTime,omitempty"`
}

// RepoListField selects which field zoekt populates in a list response.
type RepoListField int

// RepoListFieldRepos is zoekt's default: populate RepoList.Repos.
const RepoListFieldRepos RepoListField = 0

// ListOptions is the subset of zoekt.ListOptions this client sets.
type ListOptions struct {
	Field RepoListField `json:"Field"`
}

// Location is a position within a file.
type Location struct {
	ByteOffset uint32 `json:"ByteOffset"`
	LineNumber uint32 `json:"LineNumber"`
	Column     uint32 `json:"Column"`
}

// Range is a half-open span between two locations.
type Range struct {
	Start Location `json:"Start"`
	End   Location `json:"End"`
}

// Symbol is ctags metadata for a matched definition.
type Symbol struct {
	Sym        string `json:"Sym"`
	Kind       string `json:"Kind"`
	Parent     string `json:"Parent"`
	ParentKind string `json:"ParentKind"`
}

// ChunkMatch is a contiguous run of complete lines containing one or more
// matches. Content is base64 on the wire and decoded by encoding/json.
type ChunkMatch struct {
	Content      []byte    `json:"Content"`
	ContentStart Location  `json:"ContentStart"`
	Ranges       []Range   `json:"Ranges"`
	SymbolInfo   []*Symbol `json:"SymbolInfo"`
	FileName     bool      `json:"FileName"`
}

// FileMatch is one file containing matches.
type FileMatch struct {
	FileName     string       `json:"FileName"`
	Repository   string       `json:"Repository"`
	Language     string       `json:"Language"`
	Version      string       `json:"Version"`
	Branches     []string     `json:"Branches"`
	ChunkMatches []ChunkMatch `json:"ChunkMatches"`
	Content      []byte       `json:"Content"`
}

// Stats reports what a search touched.
type Stats struct {
	MatchCount    int   `json:"MatchCount"`
	FileCount     int   `json:"FileCount"`
	FilesSkipped  int   `json:"FilesSkipped"`
	ShardsSkipped int   `json:"ShardsSkipped"`
	Duration      int64 `json:"Duration"`
}

// SearchResult is the payload of a search response.
//
// zoekt embeds Stats in this struct rather than naming the field, so on the
// wire its counters arrive flattened alongside Files instead of nested under a
// "Stats" key. Embedding it here reproduces that; a named field silently
// decodes every counter as zero.
type SearchResult struct {
	Stats
	Files []FileMatch `json:"Files"`
}

// RepositoryBranch is one indexed branch of a repository.
type RepositoryBranch struct {
	Name    string `json:"Name"`
	Version string `json:"Version"`
}

// Repository is the indexed metadata for one repository.
type Repository struct {
	Name string `json:"Name"`
	URL  string `json:"URL"`
	// Source is the directory the shard was built from. Sourcebot indexes bare
	// clones under its own data directory and names them by an internal id, so
	// this is the only mapping from the repository name a caller knows to the
	// clone the git tools have to open.
	Source     string             `json:"Source"`
	Branches   []RepositoryBranch `json:"Branches"`
	HasSymbols bool               `json:"HasSymbols"`
}

// IndexMetadata records when a shard was built.
type IndexMetadata struct {
	IndexTime time.Time `json:"IndexTime"`
}

// RepoStats counts the contents of a shard set.
type RepoStats struct {
	Documents    int   `json:"Documents"`
	ContentBytes int64 `json:"ContentBytes"`
}

// RepoListEntry is one repository in a list response.
type RepoListEntry struct {
	Repository    Repository    `json:"Repository"`
	IndexMetadata IndexMetadata `json:"IndexMetadata"`
	Stats         RepoStats     `json:"Stats"`
}

// RepoList is the payload of a list response.
type RepoList struct {
	Repos []*RepoListEntry `json:"Repos"`
	Stats RepoStats        `json:"Stats"`
}

type searchArgs struct {
	Q    string         `json:"Q"`
	Opts *SearchOptions `json:"Opts,omitempty"`
}

type searchReply struct {
	Result *SearchResult `json:"Result"`
}

type listArgs struct {
	Q    string       `json:"Q"`
	Opts *ListOptions `json:"Opts,omitempty"`
}

type listReply struct {
	List *RepoList `json:"List"`
}

type errorReply struct {
	Error string `json:"Error"`
}

// Client talks to a zoekt-webserver over its JSON API.
type Client struct {
	baseURL string
	httpc   *http.Client
}

// New returns a client for the zoekt-webserver rooted at baseURL. The address
// is the server root, not the /api prefix.
func New(baseURL string, timeout time.Duration) (*Client, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return nil, fmt.Errorf("zoekt URL must not be empty")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("parse zoekt URL %q: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("zoekt URL must be http or https, got %q", baseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("zoekt URL must include a host, got %q", baseURL)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("zoekt timeout must be positive")
	}
	return &Client{baseURL: trimmed, httpc: &http.Client{Timeout: timeout}}, nil
}

// Search runs a zoekt query and returns the matching files.
func (c *Client) Search(ctx context.Context, query string, opts *SearchOptions) (*SearchResult, error) {
	var reply searchReply
	if err := c.post(ctx, "/api/search", searchArgs{Q: query, Opts: opts}, &reply); err != nil {
		return nil, err
	}
	if reply.Result == nil {
		return &SearchResult{}, nil
	}
	return reply.Result, nil
}

// List returns the repositories matching a zoekt query.
func (c *Client) List(ctx context.Context, query string, opts *ListOptions) (*RepoList, error) {
	var reply listReply
	if err := c.post(ctx, "/api/list", listArgs{Q: query, Opts: opts}, &reply); err != nil {
		return nil, err
	}
	if reply.List == nil {
		return &RepoList{}, nil
	}
	return reply.List, nil
}

func (c *Client) post(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s request: %w", path, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s request: %w", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.httpc.Do(request)
	if err != nil {
		return fmt.Errorf("call zoekt %s: %w", path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxErrorBody))
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("zoekt %s: %s", path, describeFailure(response))
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// describeFailure prefers zoekt's own error message over the raw body.
func describeFailure(response *http.Response) string {
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxErrorBody))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		return response.Status
	}
	var decoded errorReply
	if json.Unmarshal(raw, &decoded) == nil && decoded.Error != "" {
		return fmt.Sprintf("%s: %s", response.Status, decoded.Error)
	}
	return fmt.Sprintf("%s: %s", response.Status, strings.TrimSpace(string(raw)))
}
