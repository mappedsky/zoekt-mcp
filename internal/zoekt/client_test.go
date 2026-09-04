package zoekt

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewRejectsUnusableURLs(t *testing.T) {
	for _, raw := range []string{"", "   ", "ftp://example", "http://"} {
		if _, err := New(raw, time.Second); err == nil {
			t.Fatalf("New(%q) = nil error; want an error", raw)
		}
	}
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"Result":{"Files":[]}}`))
	}))
	defer server.Close()

	client, err := New(server.URL+"/", time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Search(context.Background(), "x", nil); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if gotPath != "/api/search" {
		t.Fatalf("path = %q; want /api/search", gotPath)
	}
}

func TestSearchPostsQueryAndDecodesBase64Content(t *testing.T) {
	var got searchArgs
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s; want POST", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		// Content is base64 on the wire; encoding/json decodes it into []byte.
		_, _ = w.Write([]byte(`{"Result":{"Files":[{"FileName":"main.go","Repository":"repo",` +
			`"ChunkMatches":[{"Content":"aGVsbG8=","ContentStart":{"LineNumber":7}}]}],` +
			`"Stats":{"MatchCount":1}}}`))
	}))
	defer server.Close()

	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := client.Search(context.Background(), "hello", &zoektSearchOptionsForTest)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if got.Q != "hello" {
		t.Fatalf("Q = %q; want hello", got.Q)
	}
	if got.Opts == nil || !got.Opts.ChunkMatches {
		t.Fatalf("Opts.ChunkMatches not sent: %+v", got.Opts)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files = %d; want 1", len(result.Files))
	}
	chunk := result.Files[0].ChunkMatches[0]
	if string(chunk.Content) != "hello" {
		t.Fatalf("chunk content = %q; want hello", chunk.Content)
	}
	if chunk.ContentStart.LineNumber != 7 {
		t.Fatalf("start line = %d; want 7", chunk.ContentStart.LineNumber)
	}
}

var zoektSearchOptionsForTest = SearchOptions{ChunkMatches: true}

func TestSearchSurfacesZoektErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"Error":"query: unterminated quoted string"}`))
	}))
	defer server.Close()

	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = client.Search(context.Background(), `"`, nil)
	if err == nil {
		t.Fatal("Search = nil error; want an error")
	}
	if !strings.Contains(err.Error(), "unterminated quoted string") {
		t.Fatalf("error = %v; want zoekt's message", err)
	}
}

func TestSearchToleratesAnEmptyResultEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	result, err := client.Search(context.Background(), "x", nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Files) != 0 {
		t.Fatalf("files = %d; want 0", len(result.Files))
	}
}

func TestListDecodesRepositories(t *testing.T) {
	var got listArgs
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/list" {
			t.Errorf("path = %q; want /api/list", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"List":{"Repos":[{"Repository":{"Name":"repo","HasSymbols":true,` +
			`"Branches":[{"Name":"main","Version":"abc"}]},"IndexMetadata":{"IndexTime":"2026-09-01T00:00:00Z"},` +
			`"Stats":{"Documents":12}}]}}`))
	}))
	defer server.Close()

	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	list, err := client.List(context.Background(), MatchAllRepos, &ListOptions{Field: RepoListFieldRepos})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got.Q != MatchAllRepos {
		t.Fatalf("Q = %q; want %q", got.Q, MatchAllRepos)
	}
	if len(list.Repos) != 1 || list.Repos[0].Repository.Name != "repo" {
		t.Fatalf("repos = %+v; want one named repo", list.Repos)
	}
	if list.Repos[0].Stats.Documents != 12 {
		t.Fatalf("documents = %d; want 12", list.Repos[0].Stats.Documents)
	}
}
