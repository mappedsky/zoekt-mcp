# zoekt-mcp

A Model Context Protocol server over a [zoekt](https://github.com/sourcegraph/zoekt) code
search index. It gives an agent general-purpose code search, symbol lookup, and file reads
across every repository zoekt has indexed.

It does not index anything itself. Indexing and repository sync belong to zoekt
(`zoekt-indexserver`, `zoekt-mirror-github`) or to whatever maintains the shards — this
server only reads the result through `zoekt-webserver`'s JSON API.

## Tools

Six tools, deliberately. Every tool a proxy advertises enters the model's per-turn
capability listing, so the surface is kept to what an investigation actually calls, and
capability is added as arguments to an existing tool wherever that works.

| Tool | Answers |
| --- | --- |
| `zoekt_search_code` | Where does this pattern appear? Full zoekt query syntax. |
| `zoekt_search_symbols` | Where is this symbol *defined*? Uses ctags data built at index time. |
| `zoekt_find_references` | Where is this identifier mentioned at all? Whole-word, case-sensitive. |
| `zoekt_get_file` | Read one indexed file in full. |
| `zoekt_list_files` | Which paths exist matching this pattern? No content read. |
| `zoekt_list_repos` | What is indexed, on which branches, how stale — and, with `containing`, which repositories could match a query. |

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

zoekt indexes content at a revision. It holds no commit graph, so there is no history,
blame, diff, or "when did this change" here, and there never can be — those need the git
host. It has no embeddings, so there is no semantic or natural-language search. Symbol data
is universal-ctags, so definitions are a ctags index and references are a regex, not a
resolved call graph.

## Security contract

- Every tool is a read-only query against one configured `zoekt-webserver`. No tool accepts
  a destination URL, and nothing writes.
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
| `-max-file-bytes` | 262144 | Ceiling on `zoekt_get_file`, which sets `truncated`. |
| `-search-timeout` | 20s | Server-side search deadline. |

A caller may ask for less than a ceiling but never more.

There is no response cache. Unlike a remote public API, zoekt is local, fast, and
continuously re-indexed; a cache would mostly serve stale answers about code that just
changed.

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

Every flag has an environment variable: `ZOEKT_MCP_URL`, `ZOEKT_MCP_TRANSPORT`,
`ZOEKT_MCP_HTTP_ADDRESS`, `ZOEKT_MCP_HTTP_PATH`, `ZOEKT_MCP_MAX_FILES`,
`ZOEKT_MCP_MAX_CHUNKS_PER_FILE`, `ZOEKT_MCP_CONTEXT_LINES`, `ZOEKT_MCP_MAX_FILE_BYTES`,
`ZOEKT_MCP_SEARCH_TIMEOUT`.

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
