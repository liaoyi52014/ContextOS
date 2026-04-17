# ContextOS

ContextOS is a Go-based context management middleware for agents. It provides:

- HTTP APIs for context assembly, ingest, memory search/store/delete, tool execution, tasks, and sessions
- An interactive `ctx` REPL for admin and service operations
- A Go SDK for embedded and remote use
- An MCP server over stdio for tool-based agent integration
- PostgreSQL-backed persistence and Redis-backed cache/coordination

## Requirements

- Go `1.22+`
- PostgreSQL with the `pgvector` extension available
- Redis

Current production-ready vector backend in this codebase is `pgvector`.

## Configuration

The CLI and server load configuration from:

1. `./config.yaml`
2. `$HOME/.ctx/config.yaml`
3. `/etc/contextos/config.yaml`

You can also override fields with environment variables using the `CONTEXTOS_` prefix.

Examples:

```bash
export CONTEXTOS_SERVER_PORT=9999
export CONTEXTOS_POSTGRES_DSN='postgres://postgres:password@127.0.0.1:5432/contextos?sslmode=disable&search_path=context,public'
export CONTEXTOS_REDIS_ADDR='127.0.0.1:6379'
```

The repository root already contains a sample [config.yaml](/Users/liaoyi/workspace/ContextOS/config.yaml). A typical configuration looks like this:

```yaml
server:
  url: http://localhost:9999
  port: 9999
  development_mode: false

admin:
  bootstrap_username: admin
  bootstrap_password: change-me
  username: admin
  password: change-me

redis:
  mode: standalone
  addr: 127.0.0.1:6379
  password: ""
  db: 0
  pool_size: 10
  max_retries: 3

postgres:
  dsn: postgres://postgres:password@127.0.0.1:5432/contextos?sslmode=disable&search_path=context,public

llm:
  api_base: https://dashscope.aliyuncs.com/compatible-mode
  api_key: your-llm-api-key
  model: qwen3-max
  max_tokens: 4096
  temperature: 0.2

embedding:
  api_base: https://dashscope.aliyuncs.com/compatible-mode
  api_key: your-embedding-api-key
  model: text-embedding-v4
  dimension: 1024

vector:
  backend: pgvector

engine:
  token_budget: 32000
  max_messages: 50
  compact_budget_ratio: 0.5
  compact_token_threshold: 16000
  compact_turn_threshold: 10
  compact_interval_min: 15
  compact_timeout_sec: 120
  max_concurrent_compacts: 10
  recent_raw_turn_count: 8
  recall_score_threshold: 0.7
  recall_max_results: 10
  sync_queue_size: 10000
  sync_batch_size: 100
  sync_flush_interval_ms: 500
  lru_cache_ttl_sec: 5
  slow_query_ms: 300
  skill_body_load_threshold: 0.9
  max_loaded_skill_bodies: 2

log:
  level: info
  format: json
  output: stdout
  file_path: ""
  max_size_mb: 100
  max_backups: 5
  max_age_days: 7
  component_levels: {}

migrate:
  auto_migrate: true
```

### PostgreSQL Notes

- The DSN should point to the target database and schema via `search_path`.
- ContextOS now auto-creates explicit schemas found in `search_path`, for example `context`.
- `pgvector` still must be available to the connected database user. If the user cannot create the extension, have a DBA install it first.

Recommended DSN format:

```text
postgres://<user>:<password>@<host>:<port>/<db>?sslmode=disable&search_path=context,public
```

### Redis Notes

Supported modes:

- `standalone`
- `sentinel`
- `cluster`

Examples:

```yaml
redis:
  mode: standalone
  addr: 127.0.0.1:6379
```

```yaml
redis:
  mode: sentinel
  sentinel_addrs: ["127.0.0.1:26379"]
  sentinel_master: mymaster
  sentinel_password: ""
  password: ""
  db: 0
```

```yaml
redis:
  mode: cluster
  cluster_addrs: ["127.0.0.1:7000", "127.0.0.1:7001", "127.0.0.1:7002"]
  password: ""
```

### Embedding Dimension Notes

`embedding.dimension` determines the pgvector column shape at first initialization:

- `vector_items.embedding`
- `skills.catalog_embedding`

If you change the dimension later, the code does not automatically rewrite existing column types.

Also note:

- `ivfflat` indexing fails when the vector dimension is greater than `2000`
- If you use a dimension like `2048`, the service can still run, but the vector index will not be created

## Build And Start

### 1. Validate dependencies

```bash
go run ./cmd/ctx doctor
```

This checks:

- config file loading
- PostgreSQL connectivity
- Redis connectivity
- basic LLM config presence

### 2. Run migrations

If `migrate.auto_migrate: true`, startup will run migrations automatically.

You can also run them manually:

```bash
go run ./cmd/ctx migrate up
```

Useful migration commands:

```bash
go run ./cmd/ctx migrate up
go run ./cmd/ctx migrate down
go run ./cmd/ctx migrate status
```

### 3. Start the HTTP server

```bash
go run ./cmd/ctx serve
```

Or with an explicit config path:

```bash
go run ./cmd/ctx serve --config ./config.yaml
```

Health checks:

```bash
curl http://127.0.0.1:9999/healthz
curl http://127.0.0.1:9999/readyz
curl http://127.0.0.1:9999/metrics
```

### 4. Build a binary

```bash
go build -o ./bin/ctx ./cmd/ctx
./bin/ctx doctor
./bin/ctx serve
```

## Enter The `ctx` Interactive CLI

Run the REPL:

```bash
go run ./cmd/ctx
```

Or with a built binary:

```bash
./bin/ctx
```

Behavior:

- REPL auto-loads `config.yaml`
- If `admin.username` and `admin.password` are configured, it attempts auto-login via `/api/v1/auth/login`
- Service commands such as `/session` and `/memory` require a service API key

To make service commands work, export a key created from `/apikey create`:

```bash
export CONTEXTOS_API_KEY='ctx_live_xxx'
```

Optional tenant and user overrides:

```bash
go run ./cmd/ctx --tenant tenant-a --user user-1
```

## CLI Commands

### Global commands

```bash
ctx serve
ctx mcp
ctx migrate up
ctx migrate down
ctx migrate status
ctx doctor
ctx version
ctx
```

### REPL commands

Inside the REPL, type `/help` to list commands.

#### `/admin`

Manage admin users and login state.

```text
/admin login <username> <password>
/admin list
/admin create <username> <password>
/admin update-password <id> <password>
/admin disable <id>
```

#### `/provider`

Manage model providers.

```text
/provider list
/provider add <name> <api_base> <api_key>
/provider update <id> <name> <api_base> <api_key>
/provider remove <id>
```

#### `/model`

Manage models.

```text
/model list
/model add <name> <provider_id> <model_id> <type> [dimension]
/model enable <id>
/model disable <id>
/model default <id>
```

`type` should be one of:

- `llm`
- `embedding`

#### `/skill`

Manage skills.

```text
/skill list
/skill info <id>
/skill add <json-file>
/skill enable <id>
/skill disable <id>
/skill remove <id>
```

`/skill add` currently expects a JSON file matching `SkillDocument`, for example:

```json
{
  "name": "review-skill",
  "description": "Review code changes",
  "body": "You are a code review assistant.",
  "tools": [
    {
      "name": "echo",
      "description": "Echo input",
      "input_schema": {
        "type": "object",
        "properties": {
          "text": { "type": "string" }
        }
      },
      "binding": "builtin:echo"
    }
  ]
}
```

#### `/session`

Service API key required.

```text
/session list
/session delete <id>
```

#### `/memory`

Service API key required.

```text
/memory search <query>
/memory store <content>
/memory delete <id>
```

#### `/search`

Shortcut to `/memory search`.

```text
/search <query>
```

#### `/apikey`

Manage service API keys.

```text
/apikey list
/apikey create <name>
/apikey revoke <id>
```

After `/apikey create`, the returned key is also cached in the current REPL session for subsequent service calls.

#### `/migrate`

Run local migration commands from the REPL.

```text
/migrate up
/migrate down
/migrate status
```

#### `/status`

Query observer endpoints.

```text
/status system
/status queue
/status tasks
```

#### `/logs`

```text
/logs
```

Current behavior:

- remote log query is not supported by the current server API

#### `/help`

```text
/help
/help /model
```

#### `/logout`

```text
/logout
```

#### `/exit`

```text
/exit
```

## HTTP API For Agents

Service APIs require:

- `X-API-Key`
- optional `X-Tenant-ID`
- optional `X-User-ID`

Admin APIs require:

- `Authorization: Bearer <admin-token>`

### Auth

```text
POST /api/v1/auth/setup
POST /api/v1/auth/login
POST /api/v1/auth/verify
```

### Core service APIs

```text
POST   /api/v1/context/assemble
POST   /api/v1/context/ingest
GET    /api/v1/sessions
DELETE /api/v1/sessions/:id
POST   /api/v1/memory/search
POST   /api/v1/memory/store
DELETE /api/v1/memory/:id
POST   /api/v1/tools/execute
GET    /api/v1/tasks/:task_id
POST   /api/v1/uploads/temp
```

### Admin APIs

```text
GET    /api/v1/admin/users
POST   /api/v1/admin/users
PUT    /api/v1/admin/users/:id/password
PUT    /api/v1/admin/users/:id/disable

GET    /api/v1/admin/apikeys
POST   /api/v1/admin/apikeys
DELETE /api/v1/admin/apikeys/:id

GET    /api/v1/admin/providers
POST   /api/v1/admin/providers
PUT    /api/v1/admin/providers/:id
DELETE /api/v1/admin/providers/:id

GET    /api/v1/admin/models
POST   /api/v1/admin/models
PUT    /api/v1/admin/models/:id/enable
PUT    /api/v1/admin/models/:id/disable
PUT    /api/v1/admin/models/:id/default
```

### Skills, observer, usage, and webhooks

```text
GET    /api/v1/skills/
POST   /api/v1/skills/
GET    /api/v1/skills/:id
PUT    /api/v1/skills/:id/enable
PUT    /api/v1/skills/:id/disable
DELETE /api/v1/skills/:id

GET    /api/v1/observer/system
GET    /api/v1/observer/queue

GET    /api/v1/usage/tokens

GET    /api/v1/webhooks/
POST   /api/v1/webhooks/
DELETE /api/v1/webhooks/:id
```

### Example: assemble context

```bash
curl -X POST http://127.0.0.1:9999/api/v1/context/assemble \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: ctx_live_xxx' \
  -H 'X-Tenant-ID: tenant-a' \
  -H 'X-User-ID: user-1' \
  -d '{
    "session_id": "sess-001",
    "query": "Summarize the current context",
    "token_budget": 12000,
    "telemetry": { "summary": true }
  }'
```

### Example: ingest messages

```bash
curl -X POST http://127.0.0.1:9999/api/v1/context/ingest \
  -H 'Content-Type: application/json' \
  -H 'X-API-Key: ctx_live_xxx' \
  -H 'X-Tenant-ID: tenant-a' \
  -H 'X-User-ID: user-1' \
  -d '{
    "session_id": "sess-001",
    "messages": [
      {
        "role": "user",
        "content": "Remember that I prefer concise answers."
      }
    ]
  }'
```

## Go SDK Integration

There are two SDK modes:

- embedded mode: call a local `types.Engine` directly
- remote mode: call the HTTP API

### Remote SDK example

```go
package main

import (
	"context"
	"fmt"

	"github.com/contextos/contextos/internal/sdk"
)

func main() {
	client := sdk.NewRemoteSDK("http://127.0.0.1:9999", "ctx_live_xxx")

	resp, err := client.Assemble(
		context.Background(),
		"tenant-a",
		"user-1",
		"sess-001",
		"Summarize the current context",
		12000,
	)
	if err != nil {
		panic(err)
	}

	fmt.Println(resp.SystemPrompt)
}
```

### Embedded SDK example

```go
embedded := sdk.NewEmbeddedSDK(engineInstance)
```

The remote SDK supports:

- `Assemble`
- `Ingest`
- `SearchMemory`
- `StoreMemory`
- `ForgetMemory`
- `ExecuteTool`

## MCP Integration

ContextOS exposes a stdio MCP server:

```bash
go run ./cmd/ctx mcp
```

This starts an MCP server over stdin/stdout using the same runtime configuration as `ctx serve`.

Available MCP tools:

- `context_assemble`
- `memory_search`
- `memory_store`
- `memory_forget`
- `session_summary`

### MCP tool input conventions

Most tools accept:

- `tenant_id`
- `user_id`
- `session_id`

Examples:

- `context_assemble`
  - required: `tenant_id`, `user_id`, `session_id`, `query`
  - optional: `token_budget`
- `memory_search`
  - required: `tenant_id`, `user_id`, `query`
  - optional: `limit`
- `memory_store`
  - required: `tenant_id`, `user_id`, `content`
  - optional: `metadata`
- `memory_forget`
  - required: `tenant_id`, `user_id`, `memory_id`
- `session_summary`
  - required: `tenant_id`, `user_id`, `session_id`

## Agent Integration Patterns

### Pattern 1: HTTP agent

Use the HTTP API when your agent runs out-of-process and can attach:

- `X-API-Key`
- `X-Tenant-ID`
- `X-User-ID`

Recommended flow:

1. `POST /api/v1/context/assemble`
2. call your LLM
3. `POST /api/v1/context/ingest`
4. optionally `POST /api/v1/memory/search`
5. optionally `POST /api/v1/tools/execute`

### Pattern 2: Embedded agent

Use the embedded SDK when your agent is inside the same Go process and can share the engine instance directly.

This avoids HTTP overhead and is useful for internal services.

### Pattern 3: MCP-connected agent

Use `ctx mcp` when your host environment expects MCP tools over stdio.

This is currently the best MCP integration path in the repository.

## Current Operational Notes

- If `GIN` starts in debug mode, you can switch to release mode with:

```bash
export GIN_MODE=release
```

- If the configured embedding dimension is greater than `2000`, `ivfflat` index creation will warn and be skipped by PostgreSQL.
- `pgvector` must be available in the target database.
- `/logs` exists in the REPL, but the server does not yet implement remote log querying.
- The codebase contains placeholder backends for Elasticsearch and Milvus, but the currently working vector backend is `pgvector`.

## Quick Start

```bash
go run ./cmd/ctx doctor
go run ./cmd/ctx migrate up
go run ./cmd/ctx serve
```

In another terminal:

```bash
go run ./cmd/ctx
```

Then inside the REPL:

```text
/admin list
/apikey create local-dev
/status system
```
