# zoekt-mcp

A Model Context Protocol server over a [zoekt](https://github.com/sourcegraph/zoekt) code
search index. It gives an agent general-purpose code search, symbol lookup and file reads
across every indexed repository, plus commit history, diffs and blame read straight from the
clones the index was built from.

It does not index or clone anything itself. Indexing and repository sync belong to zoekt
(`zoekt-indexserver`, `zoekt-mirror-github`) or to whatever maintains the shards — this
server reads the result through `zoekt-webserver`'s JSON API and, for history, opens the
same repositories read-only with [go-git](https://github.com/go-git/go-git). No git binary,
no subprocess, and no ownership check, so it runs from a scratch image against a read-only
mount owned by another user.

## Tools

Every tool a proxy advertises enters the model's per-turn capability listing, so the surface
is kept to what an investigation actually calls, and capability is added as arguments to an
existing tool wherever that works.

Search, always registered:

| Tool | Answers |
| --- | --- |
| `zoekt_search_code` | Where does this pattern appear? Full zoekt query syntax. |
| `zoekt_search_symbols` | Where is this symbol *defined*? Uses ctags data built at index time. |
| `zoekt_find_references` | Where is this identifier mentioned at all? Whole-word, case-sensitive. |
| `zoekt_get_file` | Read one indexed file in full. |
| `zoekt_list_files` | Which paths exist matching this pattern? No content read. |
| `zoekt_list_repos` | What is indexed, on which branches, how stale — and, with `containing`, which repositories could match a query. |

History, registered only when `-repos-root` is set:

| Tool | Answers |
| --- | --- |
| `git_log` | Which commits touched this, and when? Path-scoped. |
| `git_show` | What did this commit change? Per-file counts, diff on request. |
| `git_diff` | What moved between these two revisions? |
| `git_blame` | Who last touched these lines, and in which commit? |
| `git_file` | What did this file look like at *any* revision, including tags the index never covered? |
| `git_refs` | Which branches and tags does the clone hold? |

Without a repository root the server is search-only, which is the right shape for a
deployment that reaches zoekt over the network but cannot see the volume the clones sit on.

### How a repository name reaches a clone

The two halves address repositories by the same name, and that is not free. A clone made by
an indexing product is typically bare, has its origin URL removed so no credential is
persisted, and lives in a directory named by an internal database id — nothing on the
filesystem says which repository it is.

zoekt records both the configured name and the directory it indexed, so `/api/list` is the
mapping: `Name` is what the caller passes, `Source` is what the history tools open. The
result is cached (`-repo-path-ttl`, default 10 minutes) because clone directories are stable
across re-indexes, and a name the cache has not seen forces an immediate refresh. Every
resolved path must sit under `-repos-root`; a path outside it is refused rather than opened.

### Sweep before you read

`zoekt_list_repos` with `containing` answers "which repositories mention this" from shard
metadata, returning no file content at all. Narrowing to two repositories before searching
them costs a fraction of what searching the whole organization and discarding the misses
does, which is the difference between an investigation that fits in a turn and one that does
not.

It is a **candidate** set: zoekt decides per shard, so treat a hit as "worth searching"
rather than as proof of a match, and confirm with `zoekt_search_code`. A miss is reliable.

All five are annotated `readOnlyHint: true`, `destructiveHint: false`, `idempotentHint: true`,
`openWorldHint: false`. The index is a fixed internal corpus, not the open web.

A result's `path_match` marks a file whose *path* matched rather than its content, so a
`file:` query does not come back looking like a content hit on line 1.

`zoekt_find_references` is a **textual** search, not a resolved reference set: it returns
definitions, call sites, imports, strings, and comments alike. This is the same heuristic
search-backed code navigation uses elsewhere. Read the cited lines before drawing a
conclusion from it.

## What this cannot answer

Everything here comes from an index and a git clone, so anything that lives only in the
forge is out of reach: pull requests, issues, reviews, CI results and release notes are not
in a clone and never will be. There are no embeddings, so no semantic or natural-language
search. Symbol data is universal-ctags, so definitions are a ctags index and references are
a regex, not a resolved call graph.

Two different staleness models apply, and confusing them produces confident wrong answers:

- **Search** sees the last *indexed* revision of the branches the indexer was told to cover.
- **History** sees the last *fetched* state of every branch in the clone, which is usually
  more branches and is at least as fresh.

Neither is live. A commit pushed minutes ago is invisible to both until the next sync, so a
verdict from these tools is a verdict about a specific indexed revision — `zoekt_list_repos`
reports `indexed_at` so a caller can say which.

## Security contract

- Every tool is a read-only query against one configured `zoekt-webserver`. No tool accepts
  a destination URL, and nothing writes.
- History is read through go-git with no write paths at all; the repositories can and should
  be mounted read-only.
- **A caller never supplies a filesystem path.** Every history tool takes a repository *name*
  and resolves it through the index, so a tool argument cannot name a file to open. Resolved
  paths are then confined to `-repos-root`, compared after resolving symlinks, so a link
  inside the root that points outside it is refused.
- Tool arguments should be treated as attacker-influenceable. Indexed source is untrusted
  content, a model reads it, and the same model chooses these arguments — so every bound
  below is a real limit rather than a nicety.
- The server has **no authentication of its own**. Whoever can reach the port can read
  everything in the index. When the index holds private source, the network boundary is the
  only control — bind it to loopback or a cluster-internal address and put nothing in front
  of it that you would not let read every indexed repository.
- Indexed file content is untrusted data. It is returned as tool output, never interpreted.

## Bounds

Results are bounded so one call cannot exhaust a model turn. Every bound is a flag:

| Flag | Default | Purpose |
| --- | --- | --- |
| `-max-files` | 30 | Files returned by one search. |
| `-max-chunks-per-file` | 10 | Match chunks reported per file; the remainder is counted in `elided_chunks`. |
| `-context-lines` | 2 | Lines of context around each match. |
| `-max-file-bytes` | 262144 | Ceiling on `zoekt_get_file` and `git_file`, which sets `truncated`. |
| `-search-timeout` | 20s | Server-side search deadline. |
| `-max-commits` | 50 | Commits returned by one `git_log`. |
| `-max-patch-bytes` | 131072 | Ceiling on a returned unified diff. |
| `-max-blame-lines` | 2000 | Lines returned by one `git_blame`. |
| `-repo-path-ttl` | 10m | How long a name-to-clone mapping is reused. |
| `-max-blame-file-lines` | 50000 | Refuse to blame a file longer than this. |
| `-git-timeout` | 30s | Deadline for one git operation. |

Most limits bound the response. Two bound the *work*, which is a different thing and the
reason they exist separately:

- `git_blame` computes attribution for the whole file however few lines are requested, so a
  line range cannot bound it. `-max-blame-file-lines` refuses an over-large file instead, and
  reports the real line count so a caller can narrow or the limit can be retuned.
- A path-scoped `git_log` walks all of history when nothing matches, and a diff between
  distant revisions costs the distance. `-git-timeout` bounds every git operation, and the
  walk checks for cancellation as it goes, so a caller that gives up stops the work.

`git_show` and `git_diff` omit the patch text unless `include_patch` is set: the per-file
line counts answer most questions and a diff is the largest thing either can return.

A caller may ask for less than a ceiling but never more.

There is no response cache. Unlike a remote public API, zoekt is local, fast, and
continuously re-indexed; a cache would mostly serve stale answers about code that just
changed.

## Fuzzing

`go test -fuzz` covers the parts where a wrong answer is silent rather than loud:

| Target | Property |
| --- | --- |
| `FuzzExactAtomEncodesOneToken` | An encoded value tokenizes as exactly one atom and decodes to the intended regexp. Unquoted whitespace would split it and silently change what the query means. |
| `FuzzPlainAtomEncodesOneToken` | A plain atom decodes unchanged — escaping `lang:` or `branch:` makes them match nothing at all. |
| `FuzzSearchDecodesArbitraryResponses` | A wrong or wedged upstream cannot panic this process. |
| `FuzzOpenStaysInsideTheRoot` | No path input opens a repository whose real path is outside the root. |
| `FuzzBlameRangesAreBounded` | No line range returns lines outside the file. |

CI runs a short pass on every change. A longer campaign belongs in a scheduled run:

```sh
go test ./internal/zoekt/ -run=X -fuzz=FuzzExactAtomEncodesOneToken -fuzztime=10m
```

## Query syntax

`zoekt_search_code` takes zoekt query syntax directly. Bare terms are regular expressions
over file content; atoms narrow the search:

```text
yaml.load lang:python -file:_test\.py
sym:ParseConfig repo:^github\.com/example/repo$
"http.Client" file:\.go$ case:yes
```

The `repos`, `languages`, and `branch` arguments are conveniences that append the
corresponding atoms. They exist because the atoms are not uniform, and getting them wrong
fails **silently**:

- `repo:` and `file:` are regular expressions, so a literal value must be escaped and
  anchored.
- `lang:` is a name/alias table lookup that compiles to a constant *false* on a miss, so
  `lang:^python$` matches nothing at all rather than erroring.
- `branch:` is a substring test against the branch name, so `branch:^main$` looks for that
  literal text inside the name and never matches. `branch:main` also admits `main-old`.

`internal/zoekt/query.go` encodes each kind correctly; prefer the arguments over hand-written
atoms.

## Run locally

### Against a local repository

`docker-compose.yml` and the Makefile bring up the whole path — index, serve, expose — over
any git repository on disk. `REPO_PATH` defaults to this one:

```sh
make smoke                                   # index this repo, start, exercise every tool
make down                                    # stop and drop the index volume
```

Point it at any checkout with `REPO_PATH`. The smoke script discovers the repository name and
a sample file from the index, so only its two content probes are tuned to this repository;
override them for another corpus:

```sh
make smoke REPO_PATH=../some-checkout SMOKE_TERM=Handler SMOKE_SYMBOL=ServeHTTP
```

Those two probes report zero results rather than failing, because an empty result set is a
fact about the corpus; only a protocol or tool error fails the run.

`make index` re-indexes, `make up` starts, `make logs` follows, and `make reindex` refreshes
the shard and restarts the server. `scripts/smoke.sh` runs against whatever is already up.
The repository needs at least one commit: zoekt indexes git objects, so a checkout with no
commits has nothing to index.

**The index is built from committed objects, not the working tree.** Uncommitted changes are
invisible to every tool here, and a file reads back at its last committed revision. That is
zoekt's model rather than a property of this harness, and it holds in production too: an
answer describes the last indexed commit of a branch, so a reachability verdict is a verdict
about that revision. `zoekt_list_repos` reports `indexed_at` for exactly this reason.

### Against an existing server

The JSON API is **only served with `-rpc`**:

```sh
zoekt-webserver -index /data -listen :6070 -rpc
```

```sh
# stdio transport (default)
go run ./cmd/zoekt-mcp -zoekt-url http://localhost:6070

# stateless streamable HTTP
go run ./cmd/zoekt-mcp -transport http -http-address 127.0.0.1:8080 -zoekt-url http://localhost:6070
```

Add `-repos-root` to enable the history tools. It must be a directory containing the clones
zoekt indexed, mounted at the same path the indexer saw them at, since the paths come from
the index rather than from configuration:

```sh
go run ./cmd/zoekt-mcp -zoekt-url http://localhost:6070 -repos-root /data/.sourcebot/repos
```

Every flag has an environment variable: `ZOEKT_MCP_URL`, `ZOEKT_MCP_TRANSPORT`,
`ZOEKT_MCP_HTTP_ADDRESS`, `ZOEKT_MCP_HTTP_PATH`, `ZOEKT_MCP_MAX_FILES`,
`ZOEKT_MCP_MAX_CHUNKS_PER_FILE`, `ZOEKT_MCP_CONTEXT_LINES`, `ZOEKT_MCP_MAX_FILE_BYTES`,
`ZOEKT_MCP_SEARCH_TIMEOUT`, `ZOEKT_MCP_REPOS_ROOT`, `ZOEKT_MCP_MAX_COMMITS`,
`ZOEKT_MCP_MAX_PATCH_BYTES`, `ZOEKT_MCP_MAX_BLAME_LINES`, `ZOEKT_MCP_REPO_PATH_TTL`.

### Stdio client configuration

```json
{
  "mcpServers": {
    "zoekt": {
      "command": "zoekt-mcp",
      "args": ["-zoekt-url", "http://localhost:6070"]
    }
  }
}
```

## Run with Docker

```sh
docker build -t zoekt-mcp .
docker run --rm -p 8080:8080 zoekt-mcp \
  -transport http -http-address 0.0.0.0:8080 -zoekt-url http://zoekt-webserver:6070
```

The image default binds to loopback, which nothing outside the container can reach, so a
container serving HTTP must override `-http-address`.

## Protocol

Stateless streamable HTTP, MCP protocol version `2026-07-28`. Stateless mode is required for
that version over HTTP: each request carries its own protocol version and client
capabilities, so no session is held between calls and any replica can serve any request.
