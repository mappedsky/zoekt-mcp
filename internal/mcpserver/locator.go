package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mappedsky/zoekt-mcp/internal/zoekt"
)

// repoLocator maps the repository name a caller knows to the clone on disk.
//
// The index is the only thing that holds this mapping: a Sourcebot clone is
// bare, has its origin URL removed, and lives in a directory named by an
// internal database id, so nothing on the filesystem identifies it. zoekt
// records both the configured name and the directory it indexed, which is what
// makes the git tools addressable by name instead of by path.
type repoLocator struct {
	client Searcher
	ttl    time.Duration

	mu     sync.Mutex
	paths  map[string]string
	loaded time.Time
}

func newRepoLocator(client Searcher, ttl time.Duration) *repoLocator {
	return &repoLocator{client: client, ttl: ttl}
}

// Path returns the clone directory for a repository name, refreshing the
// mapping when it is missing or stale. Ids are stable across re-indexes, so a
// long TTL is enough; a name the cache has never seen forces a refresh.
func (l *repoLocator) Path(ctx context.Context, name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("repo must not be empty")
	}

	if path, ok := l.lookup(trimmed, false); ok {
		return path, nil
	}
	if err := l.refresh(ctx); err != nil {
		return "", err
	}
	if path, ok := l.lookup(trimmed, true); ok {
		return path, nil
	}
	return "", fmt.Errorf("repository %q is not in the index, or the index records no source path for it; call zoekt_list_repos for the names that are", name)
}

func (l *repoLocator) lookup(name string, ignoreTTL bool) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.paths == nil {
		return "", false
	}
	if !ignoreTTL && l.ttl > 0 && time.Since(l.loaded) > l.ttl {
		return "", false
	}
	path, ok := l.paths[name]
	return path, ok
}

func (l *repoLocator) refresh(ctx context.Context) error {
	list, err := l.client.List(ctx, zoekt.MatchAllRepos, &zoekt.ListOptions{Field: zoekt.RepoListFieldRepos})
	if err != nil {
		return fmt.Errorf("resolve repository paths: %w", err)
	}
	paths := make(map[string]string, len(list.Repos))
	for _, entry := range list.Repos {
		if entry == nil || entry.Repository.Source == "" {
			continue
		}
		paths[entry.Repository.Name] = entry.Repository.Source
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = paths
	l.loaded = time.Now()
	return nil
}

// known lists the resolvable repository names, for error messages.
func (l *repoLocator) known() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	names := make([]string, 0, len(l.paths))
	for name := range l.paths {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
