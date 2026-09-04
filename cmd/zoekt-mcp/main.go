package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mappedsky/zoekt-mcp/internal/mcpserver"
	"github.com/mappedsky/zoekt-mcp/internal/zoekt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	defaultTransport   = "stdio"
	defaultHTTPAddress = "127.0.0.1:8080"
	defaultHTTPPath    = "/mcp"
	defaultZoektURL    = "http://localhost:6070"
	shutdownTimeout    = 10 * time.Second
	// requestTimeout bounds one HTTP call to zoekt. It sits above the
	// server-side search deadline so a slow search returns zoekt's own error
	// rather than a client-side cancellation.
	requestTimeout = 60 * time.Second
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("zoekt-mcp: ")
	log.SetOutput(os.Stderr)

	defaults := mcpserver.DefaultConfig()

	zoektURL := flag.String("zoekt-url", envOrDefault("ZOEKT_MCP_URL", defaultZoektURL), "base URL of the zoekt-webserver to search")
	transport := flag.String("transport", envOrDefault("ZOEKT_MCP_TRANSPORT", defaultTransport), "MCP transport: stdio or http")
	httpAddress := flag.String("http-address", envOrDefault("ZOEKT_MCP_HTTP_ADDRESS", defaultHTTPAddress), "streamable HTTP listen address")
	httpPath := flag.String("http-path", envOrDefault("ZOEKT_MCP_HTTP_PATH", defaultHTTPPath), "streamable HTTP endpoint path")
	maxFiles := flag.Int("max-files", intFromEnv("ZOEKT_MCP_MAX_FILES", defaults.MaxFiles), "maximum files returned by one search")
	maxChunks := flag.Int("max-chunks-per-file", intFromEnv("ZOEKT_MCP_MAX_CHUNKS_PER_FILE", defaults.MaxChunksPerFile), "maximum match chunks reported for one file")
	contextLines := flag.Int("context-lines", intFromEnv("ZOEKT_MCP_CONTEXT_LINES", defaults.ContextLines), "lines of context around each match")
	maxFileBytes := flag.Int("max-file-bytes", intFromEnv("ZOEKT_MCP_MAX_FILE_BYTES", defaults.MaxFileBytes), "maximum bytes returned when reading a whole file")
	searchTimeout := flag.Duration("search-timeout", durationFromEnv("ZOEKT_MCP_SEARCH_TIMEOUT", defaults.SearchTimeout), "server-side search deadline")
	showVersion := flag.Bool("version", false, "print the server version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(mcpserver.Version)
		return
	}

	config := mcpserver.Config{
		MaxFiles:         *maxFiles,
		MaxChunksPerFile: *maxChunks,
		ContextLines:     *contextLines,
		MaxFileBytes:     *maxFileBytes,
		SearchTimeout:    *searchTimeout,
	}
	if err := validate(config); err != nil {
		log.Fatal(err)
	}

	client, err := zoekt.New(*zoektURL, requestTimeout)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := mcpserver.New(client, config)
	if err := run(ctx, server, *transport, *httpAddress, *httpPath); err != nil {
		log.Fatal(err)
	}
}

func validate(config mcpserver.Config) error {
	if config.MaxFiles <= 0 {
		return fmt.Errorf("max files must be positive")
	}
	if config.MaxChunksPerFile <= 0 {
		return fmt.Errorf("max chunks per file must be positive")
	}
	if config.ContextLines < 0 {
		return fmt.Errorf("context lines must not be negative")
	}
	if config.MaxFileBytes <= 0 {
		return fmt.Errorf("max file bytes must be positive")
	}
	if config.SearchTimeout <= 0 {
		return fmt.Errorf("search timeout must be positive")
	}
	return nil
}

func run(ctx context.Context, server *mcp.Server, transport, httpAddress, httpPath string) error {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case "stdio":
		err := server.Run(ctx, &mcp.StdioTransport{})
		if err != nil && ctx.Err() != nil {
			return nil
		}
		return err
	case "http":
		return runHTTP(ctx, server, httpAddress, httpPath)
	default:
		return fmt.Errorf("unsupported transport %q; supported transports: stdio, http", transport)
	}
}

func runHTTP(ctx context.Context, mcpServer *mcp.Server, address, path string) error {
	if strings.TrimSpace(address) == "" {
		return fmt.Errorf("HTTP address must not be empty")
	}
	handler, err := mcpserver.NewHTTPHandler(mcpServer, path)
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	defer listener.Close()

	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	shutdownDone := make(chan error, 1)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		shutdownDone <- httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("streamable HTTP listening on http://%s%s", listener.Addr(), path)
	err = httpServer.Serve(listener)
	if !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve streamable HTTP: %w", err)
	}
	if err := <-shutdownDone; err != nil {
		return fmt.Errorf("shut down streamable HTTP: %w", err)
	}
	return nil
}

func intFromEnv(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		log.Fatalf("%s must be an integer, got %q", name, raw)
	}
	return value
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		log.Fatalf("%s must be a Go duration such as 20s, got %q", name, raw)
	}
	return value
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
