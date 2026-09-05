package mcpserver

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/mappedsky/zoekt-mcp/internal/gitrepo"
	"github.com/mappedsky/zoekt-mcp/internal/zoekt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// gitFixture builds a repository under a fresh root and returns the root, the
// repository path, and a store rooted there.
func gitFixture(t *testing.T) (string, string, *gitrepo.Store) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "42")
	repository, err := git.PlainInit(path, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	tree, err := repository.Worktree()
	if err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "go.mod"), []byte("module fixture\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := tree.Add("go.mod"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := tree.Commit("Add go.mod", &git.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	store, err := gitrepo.NewStore(root)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return root, path, store
}

// indexed returns a searcher whose repository listing carries Source, which is
// the only thing that maps a repository name to its clone.
func indexed(name, source string) *fakeSearcher {
	return &fakeSearcher{list: &zoekt.RepoList{Repos: []*zoekt.RepoListEntry{{
		Repository: zoekt.Repository{Name: name, Source: source},
	}}}}
}

func TestLocatorResolvesNameToTheIndexedSourcePath(t *testing.T) {
	searcher := indexed("github.com/example/repo", "/data/repos/42")
	locator := newRepoLocator(searcher, time.Minute)

	path, err := locator.Path(context.Background(), "github.com/example/repo")
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	if path != "/data/repos/42" {
		t.Fatalf("path = %q; want the indexed Source", path)
	}
}

func TestLocatorCachesWithinTheTTL(t *testing.T) {
	searcher := indexed("repo", "/data/repos/42")
	locator := newRepoLocator(searcher, time.Minute)
	ctx := context.Background()

	for range 3 {
		if _, err := locator.Path(ctx, "repo"); err != nil {
			t.Fatalf("Path: %v", err)
		}
	}
	if searcher.lists != 1 {
		t.Fatalf("list calls = %d; want the mapping cached after 1", searcher.lists)
	}
}

// A name the cache has never seen forces a refresh, so a repository added since
// the last listing resolves without waiting for the TTL.
func TestLocatorRefreshesForAnUnknownName(t *testing.T) {
	searcher := indexed("repo", "/data/repos/42")
	locator := newRepoLocator(searcher, time.Hour)
	ctx := context.Background()

	if _, err := locator.Path(ctx, "repo"); err != nil {
		t.Fatalf("Path: %v", err)
	}
	if _, err := locator.Path(ctx, "other"); err == nil {
		t.Fatal("unknown repository resolved; want an error")
	}
	if searcher.lists != 2 {
		t.Fatalf("list calls = %d; want a refresh attempt for the unknown name", searcher.lists)
	}
}

func TestLocatorErrorPointsAtListRepos(t *testing.T) {
	locator := newRepoLocator(indexed("repo", "/data/repos/42"), time.Minute)
	_, err := locator.Path(context.Background(), "missing")
	if err == nil {
		t.Fatal("missing repository accepted; want an error")
	}
	if !contains(err.Error(), "zoekt_list_repos") {
		t.Fatalf("error = %v; want it to point at zoekt_list_repos", err)
	}
}

// A repository the index knows but records no Source for cannot be opened, and
// saying so beats resolving to an empty path.
func TestLocatorSkipsRepositoriesWithoutASource(t *testing.T) {
	searcher := &fakeSearcher{list: &zoekt.RepoList{Repos: []*zoekt.RepoListEntry{{
		Repository: zoekt.Repository{Name: "repo"},
	}}}}
	if _, err := newRepoLocator(searcher, time.Minute).Path(context.Background(), "repo"); err == nil {
		t.Fatal("repository without a Source resolved; want an error")
	}
}

func TestGitToolsAreNotRegisteredWithoutAStore(t *testing.T) {
	tools := listToolNames(t, New(&fakeSearcher{}, nil, testConfig()))
	for _, name := range tools {
		if len(name) > 4 && name[:4] == "git_" {
			t.Fatalf("history tool %q registered without a repository root", name)
		}
	}
	if len(tools) != 6 {
		t.Fatalf("tools = %d; want the 6 search tools", len(tools))
	}
}

func TestGitToolsRunAgainstTheResolvedClone(t *testing.T) {
	_, path, store := gitFixture(t)
	server := New(indexed("github.com/example/repo", path), store, testConfig())

	if got := len(listToolNames(t, server)); got != 12 {
		t.Fatalf("tools = %d; want 6 search plus 6 history", got)
	}

	session := connect(t, server)
	defer session.Close()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "git_log",
		Arguments: map[string]any{"repo": "github.com/example/repo", "path": "go.mod"},
	})
	if err != nil {
		t.Fatalf("CallTool git_log: %v", err)
	}
	if result.IsError {
		t.Fatalf("git_log returned a tool error: %+v", result.Content)
	}

	refs, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "git_refs",
		Arguments: map[string]any{"repo": "github.com/example/repo"},
	})
	if err != nil {
		t.Fatalf("CallTool git_refs: %v", err)
	}
	if refs.IsError {
		t.Fatalf("git_refs returned a tool error: %+v", refs.Content)
	}
}

func TestGitToolRejectsAnUnknownRepository(t *testing.T) {
	_, path, store := gitFixture(t)
	session := connect(t, New(indexed("known", path), store, testConfig()))
	defer session.Close()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "git_log",
		Arguments: map[string]any{"repo": "unknown"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("unknown repository accepted; want a tool error")
	}
}

func listToolNames(t *testing.T, server *mcp.Server) []string {
	t.Helper()
	session := connect(t, server)
	defer session.Close()
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	handler, err := NewHTTPHandler(server, "/mcp")
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "git-test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
	}, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	return session
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
