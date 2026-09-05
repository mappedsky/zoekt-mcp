package mcpserver

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/mappedsky/zoekt-mcp/internal/zoekt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestNewHTTPHandlerRejectsUnusableInput(t *testing.T) {
	server := New(&fakeSearcher{}, nil, testConfig())
	if _, err := NewHTTPHandler(nil, "/mcp"); err == nil {
		t.Fatal("nil server accepted; want an error")
	}
	for _, path := range []string{"", "mcp", "/mcp?x=1", "/m cp"} {
		if _, err := NewHTTPHandler(server, path); err == nil {
			t.Fatalf("NewHTTPHandler(%q) = nil error; want an error", path)
		}
	}
}

func TestStreamableHTTPNegotiates20260728AndCallsTool(t *testing.T) {
	searcher := &fakeSearcher{result: &zoekt.SearchResult{
		Files: []zoekt.FileMatch{{
			FileName:   "main.go",
			Repository: "github.com/example/repo",
			ChunkMatches: []zoekt.ChunkMatch{{
				Content:      []byte("import \"gopkg.in/yaml.v3\"\n"),
				ContentStart: zoekt.Location{LineNumber: 4},
				Ranges:       []zoekt.Range{{Start: zoekt.Location{LineNumber: 4}}},
			}},
		}},
		Stats: zoekt.Stats{MatchCount: 1},
	}}

	handler, err := NewHTTPHandler(New(searcher, nil, testConfig()), "/mcp")
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "http-test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
	}, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	if got := session.InitializeResult().ProtocolVersion; got != "2026-07-28" {
		t.Fatalf("protocol version = %q; want 2026-07-28", got)
	}

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if got := len(tools.Tools); got != 6 {
		t.Fatalf("tool count = %d; want 6", got)
	}
	for _, advertised := range tools.Tools {
		if advertised.Annotations == nil || !advertised.Annotations.ReadOnlyHint {
			t.Fatalf("tool %q is not annotated read-only", advertised.Name)
		}
	}

	// Stateless mode builds a fresh session per request, so a second call over
	// the same transport has to work as well as the first.
	for call := range 2 {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "zoekt_search_code",
			Arguments: map[string]any{"query": "yaml.v3", "repos": []any{"github.com/example/repo"}},
		})
		if err != nil {
			t.Fatalf("CallTool %d: %v", call, err)
		}
		if result.IsError {
			t.Fatalf("CallTool %d returned a tool error: %+v", call, result.Content)
		}
	}
	if searcher.searches != 2 {
		t.Fatalf("searches = %d; want 2", searcher.searches)
	}
}

func TestCallToolReportsAnEmptyQueryAsAToolError(t *testing.T) {
	handler, err := NewHTTPHandler(New(&fakeSearcher{}, nil, testConfig()), "/mcp")
	if err != nil {
		t.Fatalf("NewHTTPHandler: %v", err)
	}
	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "http-test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: httpServer.URL + "/mcp",
	}, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer session.Close()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "zoekt_search_code",
		Arguments: map[string]any{"query": "   "},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !result.IsError {
		t.Fatal("empty query accepted; want a tool error")
	}
}
