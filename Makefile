# Local development harness. REPO_PATH selects the repository to index; it
# defaults to this one.
REPO_PATH   ?= $(CURDIR)
REPO_NAME   ?= $(notdir $(patsubst %/,%,$(abspath $(REPO_PATH))))
REPO_BRANCH ?= $(shell git -C "$(REPO_PATH)" rev-parse --abbrev-ref HEAD)
MCP_PORT    ?= 8080

COMPOSE = REPO_PATH="$(abspath $(REPO_PATH))" REPO_NAME="$(REPO_NAME)" \
          REPO_BRANCH="$(REPO_BRANCH)" MCP_PORT="$(MCP_PORT)" \
          docker compose

.PHONY: test index up down logs smoke reindex

test:
	go vet ./...
	go test -race ./...

# Index REPO_PATH at its current branch into the shared volume.
index:
	@echo "==> Indexing $(REPO_NAME) ($(REPO_BRANCH)) from $(abspath $(REPO_PATH))"
	$(COMPOSE) --profile index run --rm indexer

up:
	$(COMPOSE) up -d --build zoekt mcp
	@echo "==> MCP on http://localhost:$(MCP_PORT)/mcp"

down:
	$(COMPOSE) --profile index down --volumes --remove-orphans

logs:
	$(COMPOSE) logs -f zoekt mcp

# Index, start, and exercise every tool against the result.
smoke: index up
	MCP_PORT=$(MCP_PORT) ./scripts/smoke.sh

reindex: index
	$(COMPOSE) restart zoekt
