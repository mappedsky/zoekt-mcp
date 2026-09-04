#!/usr/bin/env bash
# Exercise every tool against a running dev stack (see `make smoke`).
#
# MCP 2026-07-28 over stateless streamable HTTP carries per-request framing that
# a session would otherwise negotiate once: the protocol version and client
# capabilities travel in _meta, and the method and tool name are mirrored into
# headers. All of it is required; the server rejects a request missing any part.
#
# The repository name and a sample file are discovered from the index rather
# than hard-coded, so this runs against whatever `make index` last built. The
# two content probes default to terms that exist in this repository; override
# SMOKE_TERM and SMOKE_SYMBOL when pointing the stack at a different checkout.
set -euo pipefail

PORT="${MCP_PORT:-8080}"
ENDPOINT="http://localhost:${PORT}/mcp"
VERSION="2026-07-28"
TERM_QUERY="${SMOKE_TERM:-zoekt}"
SYMBOL="${SMOKE_SYMBOL:-NewHTTPHandler}"
META="\"_meta\":{\"io.modelcontextprotocol/protocolVersion\":\"${VERSION}\",\"io.modelcontextprotocol/clientCapabilities\":{}}"

rpc() {
  local method="$1" body="$2"
  shift 2
  curl -sS --fail-with-body --max-time 30 -X POST "$ENDPOINT" \
    -H 'Content-Type: application/json' \
    -H 'Accept: application/json, text/event-stream' \
    -H "Mcp-Protocol-Version: ${VERSION}" \
    -H "Mcp-Method: ${method}" \
    "$@" \
    -d "$body" | sed 's/^data: //' | grep -v -e '^event:' -e '^$'
}

# Returns a tool's structuredContent, failing the script on a JSON-RPC error or
# a tool error rather than letting a later step misread an error body.
call_tool() {
  local name="$1" args="$2"
  rpc tools/call \
    "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{${META},\"name\":\"${name}\",\"arguments\":${args}}}" \
    -H "Mcp-Name: ${name}" \
  | python3 -c '
import json, sys
payload = json.load(sys.stdin)
if "error" in payload:
    sys.exit("JSON-RPC error from %s: %s" % (sys.argv[1], payload["error"]))
result = payload["result"]
if result.get("isError"):
    sys.exit("tool error from %s: %s" % (sys.argv[1], result["content"][0].get("text")))
json.dump(result["structuredContent"], sys.stdout)
' "$name"
}

# field <expr> reads one value out of a structuredContent document on stdin.
field() {
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(eval(sys.argv[1], {}, {"d": d}))' "$1"
}

echo "==> Waiting for ${ENDPOINT}"
ready=""
for _ in $(seq 1 30); do
  if rpc tools/list "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{${META}}}" \
      >/dev/null 2>&1; then
    ready=yes
    break
  fi
  sleep 1
done
[ -n "$ready" ] || { echo "server did not become ready" >&2; exit 1; }

echo "==> tools/list"
rpc tools/list "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{${META}}}" | python3 -c '
import json, sys
tools = json.load(sys.stdin)["result"]["tools"]
for tool in tools:
    annotations = tool.get("annotations") or {}
    if not annotations.get("readOnlyHint"):
        sys.exit("tool %s is not annotated read-only" % tool["name"])
    print("    %s" % tool["name"])
print("    %d tools, all read-only" % len(tools))
'

echo "==> zoekt_list_repos"
repos_json="$(call_tool zoekt_list_repos '{}')"
repo="$(printf '%s' "$repos_json" | field 'd["repos"][0]["name"]')"
printf '%s' "$repos_json" | field '"    %d repo(s); first=%s branches=%s indexed_at=%s" % (d["count"], d["repos"][0]["name"], ",".join(d["repos"][0].get("branches") or []), d["repos"][0].get("indexed_at"))'

echo "==> zoekt_list_files (paths only, no content read)"
files_json="$(call_tool zoekt_list_files "{\"pattern\":\".\",\"repos\":[\"${repo}\"],\"max_files\":5}")"
sample="$(printf '%s' "$files_json" | field 'd["files"][0]["path"]')"
printf '%s' "$files_json" | field '"    %d path(s); first=%s" % (d["count"], d["files"][0]["path"])'

echo "==> zoekt_get_file (${sample})"
call_tool zoekt_get_file "{\"repo\":\"${repo}\",\"path\":\"${sample}\"}" \
  | field '"    %d bytes, %d lines, truncated=%s" % (d["bytes"], d["lines"], d.get("truncated", False))'

# A different checkout will not contain these terms. Zero results are reported
# rather than failed, because only a protocol or tool error means the server is
# broken; an empty result set is a fact about the corpus.
echo "==> zoekt_search_code (${TERM_QUERY})"
call_tool zoekt_search_code "{\"query\":\"${TERM_QUERY}\",\"max_files\":3}" \
  | field '"    %d file(s), %d match(es), truncated=%s" % (d["file_count"], d["match_count"], d["truncated"])'

echo "==> zoekt_search_symbols (${SYMBOL})"
call_tool zoekt_search_symbols "{\"symbol\":\"${SYMBOL}\",\"max_files\":3}" \
  | field '"    %d file(s); %s" % (d["file_count"], [s for f in d["files"] for c in f.get("chunks", []) for s in c.get("symbols", [])] or "no ctags symbols")'

echo "==> zoekt_find_references (${SYMBOL})"
call_tool zoekt_find_references "{\"symbol\":\"${SYMBOL}\",\"max_files\":5}" \
  | field '"    %d file(s): %s" % (d["file_count"], [f["path"] for f in d["files"]])'

echo "==> zoekt_list_repos containing=${TERM_QUERY} (candidate sweep, no content read)"
call_tool zoekt_list_repos "{\"containing\":\"${TERM_QUERY}\"}" \
  | field '"    %d candidate repo(s): %s" % (d["count"], [r["name"] for r in d["repos"]])'

echo "==> OK"
