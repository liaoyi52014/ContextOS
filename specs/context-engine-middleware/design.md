# 技术设计文档：ContextOS — Go 上下文引擎中间件

## 概述

ContextOS 是一个基于 Go 语言的 AI Agent 上下文管理中间件，为上游 Agent 应用提供统一的上下文生命周期管理能力。当前修订保留原有总体逻辑和组件边界，只修正以下几类冲突：

1. 将管理员认证与服务接入认证彻底拆开
2. 将 Skill 从文件路径真相改为数据库真相
3. 将 Skill 明确为全局共享数据，不参与租户隔离
4. 将归档数据二元组隔离、实时会话三元组隔离落到接口和数据模型
5. 统一 HTTP API、Go SDK、MCP 的参数传递和需求追踪

核心设计理念保持不变：

1. **三层渐进式加载**：L0（摘要/索引）→ L1（概览）→ L2（完整内容）
2. **无状态计算 + 共享存储**：计算节点不持有会话状态，通过 Redis + PostgreSQL 实现分布式部署
3. **接口驱动**：所有外部依赖通过 Go 接口抽象，便于测试和替换
4. **二元组归档 + 三元组会话隔离**：长期数据按 `tenant_id + user_id` 隔离，实时会话按 `tenant_id + user_id + session_id` 隔离
5. **全局共享 Skill**：Skill 不按租户隔离，统一存储在数据库中，以 `name + description` 先注入、`body` 按需加载
6. **四种接入方式**：HTTP API、Go SDK、MCP Server、Webhook，共享同一核心引擎
7. **请求级可观测性**：通过 opt-in telemetry 返回单次请求的结构化执行摘要
8. **统一任务追踪**：所有长耗时后台流程共享 TaskTracker 和查询接口

## 架构

### 整体架构图

```text
┌─────────────────────────────────────────────────────────────────────┐
│                        Agent 接入层                                  │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────────┐   │
│  │ HTTP API │  │  Go SDK  │  │MCP Server│  │ Webhook Notifier │   │
│  └────┬─────┘  └────┬─────┘  └────┬─────┘  └────────┬─────────┘   │
│       │              │             │                  │             │
│       └──────────────┴─────────────┴──────────────────┘             │
│                              │                                      │
├──────────────────────────────┼──────────────────────────────────────┤
│                    认证与上下文提取层                                 │
│  管理员认证：账号密码 -> 管理 API / CLI                               │
│  服务认证：X-API-Key / api_key -> RequestContext                     │
│  隔离标识：X-Tenant-ID / X-User-ID / session_id                      │
├──────────────────────────────┼──────────────────────────────────────┤
│                         核心引擎层                                   │
│  ┌───────────────┐  ┌────────────────┐  ┌──────────────────────┐   │
│  │ ContextBuilder│  │ SessionManager │  │  CompactProcessor    │   │
│  │ (上下文组装)   │  │ (会话管理)      │  │  (上下文压缩)        │   │
│  └───────┬───────┘  └───────┬────────┘  └──────────┬───────────┘   │
│          │                  │                       │               │
│  ┌───────┴───────┐  ┌──────┴───────┐  ┌───────────┴────────────┐  │
│  │RetrievalEngine│  │ ToolRegistry │  │    HookManager         │  │
│  │ (上下文检索)   │  │ (工具系统)    │  │    (生命周期钩子)      │  │
│  └───────┬───────┘  └──────────────┘  └────────────────────────┘  │
├──────────┼─────────────────────────────────────────────────────────┤
│          │            抽象接口层                                     │
│  ┌───────┴───────┐  ┌──────────────┐  ┌────────────────────────┐  │
│  │  VectorStore  │  │EmbeddingProv.│  │     LLMClient          │  │
│  └───────┬───────┘  └──────┬───────┘  └────────┬───────────────┘  │
│  ┌───────┴───────┐  ┌──────┴───────┐  ┌────────┴───────────────┐  │
│  │  CacheStore   │  │ SessionStore │  │     AuditStore         │  │
│  └───────┬───────┘  └──────┬───────┘  └────────┬───────────────┘  │
├──────────┼─────────────────┼────────────────────┼──────────────────┤
│          │            存储层                      │                  │
│  ┌───────┴───────┐  ┌──────┴───────┐  ┌────────┴───────────────┐  │
│  │   pgvector    │  │    Redis     │  │    PostgreSQL          │  │
│  │   / ES        │  │  (缓存/锁)   │  │  (持久化/审计)         │  │
│  │   / Milvus    │  │              │  │                        │  │
│  └───────────────┘  └──────────────┘  └────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
```

### 数据流

```text
Agent 请求 (Assemble)
  │
  ├─ 1. 服务认证：X-API-Key / api_key -> 验证服务身份
  ├─ 2. 上下文提取：tenant_id / user_id / session_id -> RequestContext
  │
  ├─ 3. ContextBuilder.Assemble()
  │     ├─ 并行阶段（errgroup）：
  │     │     ├─ goroutine 1: 加载 user profile (tenant_id + user_id)
  │     │     ├─ goroutine 2: 语义检索 memories (tenant_id + user_id)
  │     │     ├─ goroutine 3: 加载 session history (tenant_id + user_id + session_id)
  │     │     └─ goroutine 4: 加载全局 Skill 目录（name + description）
  │     ├─ 汇总阶段（串行）：
  │     │     ├─ 按相关性评分排序与编排
  │     │     ├─ 先注入 Skill 摘要（name + description）
  │     │     ├─ 对命中的 Skill 再加载 body
  │     │     ├─ 渐进升级：按评分优先级批量加载 L1/L2
  │     │     ├─ Token 裁剪：超预算时降级低优先级内容
  │     │     └─ 返回组装后的 messages + token 估算
  │
  ├─ 4. Agent 调用 LLM，获得响应
  │
  └─ 5. Agent 请求 (Ingest)
        ├─ SessionManager 写入本轮消息
        ├─ CompactProcessor 评估触发条件
        │     ├─ 未触发 -> 返回
        │     └─ 触发 -> goroutine 异步执行
        │           ├─ 获取分布式锁
        │           ├─ LLM 生成摘要
        │           ├─ 提取记忆事实
        │           ├─ 持久化 CompactCheckpoint
        │           └─ 释放锁，触发 Webhook
        └─ 返回写入确认 + compact 状态
```

### 组件职责

| 组件 | 职责 |
|------|------|
| ContextBuilder | 三层渐进式上下文组装，相关性评分编排，多来源内容编排，Skill 摘要先注入和正文按需加载 |
| SessionManager | 三层缓存（LRU -> Redis -> PG），会话 CRUD，消息历史管理 |
| CompactProcessor | 触发条件评估，异步压缩执行，分布式锁互斥，记忆提取 |
| RetrievalEngine | 语义检索 + 模式检索（keyword/grep/glob），渐进式内容加载 |
| ToolRegistry | 动态工具注册/注销，Skill/Command/MCP 三类工具管理 |
| HookManager | 事件驱动生命周期回调（afterTurn/beforePrompt/compact/tool.post_call） |
| TaskTracker | 后台任务状态机，统一追踪 compact、异步导入等长任务 |
| AdminAuth | 管理员账号密码认证、默认管理员初始化、管理态校验 |
| APIKeyManager | 服务 API Key 的颁发、校验、吊销和缓存同步 |

## 组件与接口

### 核心 Go 接口定义

```go
package contextos

import (
    "context"
    "time"
)

type RequestContext struct {
    TenantID  string `json:"tenant_id"`
    UserID    string `json:"user_id"`
    SessionID string `json:"session_id"`
}

type VectorItem struct {
    ID       string            `json:"id"`
    Vector   []float32         `json:"vector"`
    Content  string            `json:"content"`
    URI      string            `json:"uri"`
    TenantID string            `json:"tenant_id"`
    UserID   string            `json:"user_id"`
    Metadata map[string]string `json:"metadata"`
}

type Filter struct {
    TenantID string `json:"tenant_id"`
    UserID   string `json:"user_id,omitempty"`
}

type SearchQuery struct {
    Vector    []float32 `json:"vector"`
    TopK      int       `json:"top_k"`
    Filter    *Filter   `json:"filter,omitempty"`
    Threshold float64   `json:"threshold"`
}

type SearchResult struct {
    Item  VectorItem `json:"item"`
    Score float64    `json:"score"`
}

type VectorStore interface {
    Init(ctx context.Context, dimension int) error
    Upsert(ctx context.Context, items []VectorItem) error
    Search(ctx context.Context, query SearchQuery) ([]SearchResult, error)
    Delete(ctx context.Context, ids []string) error
}

type EmbeddingProvider interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimension() int
}

type Message struct {
    Role      string                 `json:"role"`
    Content   string                 `json:"content"`
    Timestamp time.Time              `json:"timestamp"`
    Metadata  map[string]interface{} `json:"metadata,omitempty"`
    ToolCalls []ToolCall             `json:"tool_calls,omitempty"`
}

type ToolCall struct {
    ID        string                 `json:"id"`
    ToolName  string                 `json:"tool_name"`
    Arguments map[string]interface{} `json:"arguments"`
    Result    string                 `json:"result,omitempty"`
    Status    string                 `json:"status"`
}

type UsageRecord struct {
    URI          string    `json:"uri,omitempty"`
    SkillName    string    `json:"skill_name,omitempty"`
    ToolName     string    `json:"tool_name,omitempty"`
    InputSummary string    `json:"input_summary,omitempty"`
    OutputSummary string   `json:"output_summary,omitempty"`
    Success      bool      `json:"success"`
    Timestamp    time.Time `json:"timestamp"`
}

type LLMRequest struct {
    Messages    []Message `json:"messages"`
    MaxTokens   int       `json:"max_tokens,omitempty"`
    Temperature float64   `json:"temperature,omitempty"`
    Model       string    `json:"model,omitempty"`
}

type LLMResponse struct {
    Content          string `json:"content"`
    PromptTokens     int    `json:"prompt_tokens"`
    CompletionTokens int    `json:"completion_tokens"`
    TotalTokens      int    `json:"total_tokens"`
    Model            string `json:"model"`
}

type LLMClient interface {
    Complete(ctx context.Context, req LLMRequest) (*LLMResponse, error)
}

type CacheStore interface {
    Get(ctx context.Context, key string) ([]byte, error)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
    Delete(ctx context.Context, key string) error
    SetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
}

type TaskStatus string

const (
    TaskPending   TaskStatus = "pending"
    TaskRunning   TaskStatus = "running"
    TaskCompleted TaskStatus = "completed"
    TaskFailed    TaskStatus = "failed"
)

type TaskRecord struct {
    ID            string                 `json:"id"`
    Type          string                 `json:"type"`
    Status        TaskStatus             `json:"status"`
    TraceID       string                 `json:"trace_id"`
    ResultSummary map[string]interface{} `json:"result_summary,omitempty"`
    Error         string                 `json:"error,omitempty"`
    CreatedAt     time.Time              `json:"created_at"`
    StartedAt     time.Time              `json:"started_at,omitempty"`
    FinishedAt    time.Time              `json:"finished_at,omitempty"`
}

type TaskTracker interface {
    Create(ctx context.Context, taskType string, payload map[string]interface{}) (*TaskRecord, error)
    Start(ctx context.Context, taskID string) error
    Complete(ctx context.Context, taskID string, result map[string]interface{}) error
    Fail(ctx context.Context, taskID string, err error) error
    Get(ctx context.Context, taskID string) (*TaskRecord, error)
    QueueStats(ctx context.Context) (map[string]interface{}, error)
}

type TelemetryOption struct {
    Summary bool `json:"summary,omitempty"`
}

type TelemetrySummary struct {
    Operation  string                 `json:"operation"`
    Status     string                 `json:"status"`
    DurationMS float64                `json:"duration_ms"`
    Tokens     map[string]interface{} `json:"tokens,omitempty"`
    Vector     map[string]interface{} `json:"vector,omitempty"`
    Skill      map[string]interface{} `json:"skill,omitempty"`
    Compact    map[string]interface{} `json:"compact,omitempty"`
    Profile    map[string]interface{} `json:"profile,omitempty"`
    Errors     []map[string]string    `json:"errors,omitempty"`
}

type OperationTelemetry struct {
    ID      string           `json:"id"`
    Summary TelemetrySummary `json:"summary"`
}

type SessionStore interface {
    Load(ctx context.Context, tenantID, userID, sessionID string) (*Session, error)
    Save(ctx context.Context, session *Session) error
    Delete(ctx context.Context, tenantID, userID, sessionID string) error
    List(ctx context.Context, tenantID, userID string) ([]*SessionMeta, error)
}

type ToolType string

const (
    ToolTypeSkill   ToolType = "skill"
    ToolTypeCommand ToolType = "command"
    ToolTypeMCP     ToolType = "mcp"
)

type Tool interface {
    Name() string
    Description() string
    Schema() map[string]interface{}
    Execute(ctx context.Context, rc RequestContext, params map[string]interface{}) (string, error)
    Type() ToolType
}

type HookEvent string

const (
    HookAfterTurn    HookEvent = "afterTurn"
    HookBeforePrompt HookEvent = "beforePrompt"
    HookCompact      HookEvent = "compact"
    HookToolPostCall HookEvent = "tool.post_call"
)

type HookContext struct {
    Event     HookEvent              `json:"event"`
    TenantID  string                 `json:"tenant_id"`
    UserID    string                 `json:"user_id"`
    SessionID string                 `json:"session_id"`
    Data      map[string]interface{} `json:"data"`
}

type Hook interface {
    Name() string
    Events() []HookEvent
    Execute(ctx context.Context, hookCtx HookContext) error
}
```

### ContextBuilder 组件设计

ContextBuilder 负责三层渐进式上下文组装，是核心引擎入口。总体逻辑保留原设计中的“两阶段：并行获取 + 串行编排”，只新增全局 Skill 摘要先注入、正文按需加载。

```go
type ContentLevel int

const (
    ContentL0 ContentLevel = iota // 摘要/索引
    ContentL1                     // 概览
    ContentL2                     // 完整内容
)

type ContentBlock struct {
    URI     string       `json:"uri"`
    Level   ContentLevel `json:"level"`
    Content string       `json:"content"`
    Source  string       `json:"source"` // memory / skill / profile / history
    Score   float64      `json:"score"`
    Tokens  int          `json:"tokens"`
}

type AssembleRequest struct {
    SessionID   string `json:"session_id"`
    Query       string `json:"query"`
    TokenBudget int    `json:"token_budget,omitempty"`
    Telemetry   *TelemetryOption `json:"telemetry,omitempty"`
}

type AssembleResponse struct {
    SystemPrompt    string         `json:"system_prompt"`
    Messages        []Message      `json:"messages"`
    EstimatedTokens int            `json:"estimated_tokens"`
    Sources         []ContentBlock `json:"sources"`
    Telemetry       *OperationTelemetry `json:"telemetry,omitempty"`
}

type ContextBuilder struct {
    vectorStore VectorStore
    embedding   EmbeddingProvider
    session     SessionStore
    profiles    ProfileStore
    cache       CacheStore
    skills      *SkillManager
    config      *ContextConfig
}

func (b *ContextBuilder) Assemble(ctx context.Context, rc RequestContext, req AssembleRequest) (*AssembleResponse, error)
```

组装流程：

1. 并行加载四类数据：
   - 用户画像：按 `tenant_id + user_id`
   - 记忆召回：按 `tenant_id + user_id`
   - 会话历史：按 `tenant_id + user_id + session_id`
   - Skill 目录：加载所有启用 Skill 的 `name + description`
2. 串行编排：
   - 按相关性和优先级对四类内容统一排序
   - 优先放入 L0 内容块
   - Skill 的 `name + description` 作为 L0 内容块参与评分和预算
   - 若某 Skill 的摘要块被判定为命中且仍有预算，则从 `skills.body` 加载正文，并将该 Skill 升级为 L2
   - 若预算不足，优先保留最新 raw turns 和高分记忆，低分 Skill 正文降级回摘要
3. 返回最终 `messages`、`sources` 和 `EstimatedTokens`

Skill 渐进式加载规则：

- Skill 默认注入层：`name + description`
- Skill 完整层：`body`
- ContextBuilder 不直接依赖文件系统，所有 Skill 数据都由 SkillManager 从数据库读取
- 同一轮组装中，一个 Skill 最多加载一次 `body`
- 若 `LoadCatalog` 返回空集合，ContextBuilder 必须直接跳过全部 Skill 相关逻辑，不得报错，也不得阻塞其他上下文来源的组装
- Skill 命中判断输入为：当前 `query` + 最近 4 轮会话消息
- ContextBuilder 为每个候选 Skill 计算 `matchScore`
- `matchScore` 由三部分组成：
  - `exact_name_hit`：query 或最近消息中直接出现 Skill 名称，权重最高
  - `keyword_hit`：query 或最近消息命中 description 关键词，作为中等权重
  - `semantic_hit`：query 与 `name + description` 的 embedding 相似度，作为兜底语义召回
- 推荐评分公式：
  - `matchScore = 1.0 * exact_name_hit + 0.5 * keyword_hit + 0.7 * semantic_hit`
  - `exact_name_hit` 取值 `0` 或 `1`
  - `keyword_hit` 归一化到 `0.0 ~ 1.0`
  - `semantic_hit` 归一化到 `0.0 ~ 1.0`
- 当 `matchScore >= skill_body_load_threshold` 时，Skill 被视为命中，允许加载 `body`
- `skill_body_load_threshold` 默认值为 `0.9`
- 同一轮上下文组装最多加载 `max_loaded_skill_bodies` 个 Skill 正文，默认值为 `2`
- 若 query 明确点名某个 Skill 名称，则该 Skill 直接视为强命中，并优先参与正文加载排序
- 对命中的 Skill 按 `matchScore` 降序排序，在 Token 预算允许时依次调用 `LoadBody`
- 若正文加载后超过 Token 预算，则回退该 Skill 正文，仅保留其 `name + description`
- 已加载过的 Skill 正文在同一轮组装内可复用，不重复读取数据库
- `keyword_hit` 的 v1 实现使用 description 文本按空白和标点切词、小写归一化、去除停用词后的词项集合匹配
- `semantic_hit` 使用 Skill 导入或更新时预计算并持久化的 `catalog_embedding`；运行时只计算当前 query 的 embedding

### UserProfile 组件设计

用户画像用于沉淀用户的长期稳定信息，本质上是从会话上下文中提炼出的持久记忆摘要，而不是额外人工维护的数据源。

```go
type ProfileStore interface {
    Load(ctx context.Context, tenantID, userID string) (*UserProfile, error)
    Upsert(ctx context.Context, profile *UserProfile) error
    Search(ctx context.Context, tenantID, userID, query string, limit int) ([]ContentBlock, error)
}
```

画像更新规则：

- 用户画像由 CompactProcessor 在会话压缩或显式 ingest 后异步提取并合并
- 提取来源为最近会话消息、既有画像摘要和长期记忆
- 合并策略采用“稳定信息保留 + 冲突信息覆写 + 低置信短期噪声丢弃”
- 画像文本作为归档上下文来源参与 Assemble，默认以 L0/L1 形式注入
- 若当前租户用户不存在画像记录，ProfileStore 返回 `nil, nil`，ContextBuilder 直接跳过画像注入而不报错

### SessionManager 组件设计

三层缓存架构保持不变：本地 LRU -> Redis -> PostgreSQL。

```go
type Session struct {
    ID          string                 `json:"id"`
    TenantID    string                 `json:"tenant_id"`
    UserID      string                 `json:"user_id"`
    Messages    []Message              `json:"messages"`
    Usage       []UsageRecord          `json:"usage,omitempty"`
    Metadata    map[string]interface{} `json:"metadata"`
    CreatedAt   time.Time              `json:"created_at"`
    UpdatedAt   time.Time              `json:"updated_at"`
    MaxMessages int                    `json:"max_messages"`
}

type SessionMeta struct {
    ID                  string         `json:"id"`
    TenantID            string         `json:"tenant_id"`
    UserID              string         `json:"user_id"`
    MessageCount        int            `json:"message_count"`
    CommitCount         int            `json:"commit_count"`
    ContextsUsed        int            `json:"contexts_used"`
    SkillsUsed          int            `json:"skills_used"`
    ToolsUsed           int            `json:"tools_used"`
    LLMTokenUsage       map[string]int `json:"llm_token_usage"`
    EmbeddingTokenUsage map[string]int `json:"embedding_token_usage"`
    CreatedAt           time.Time      `json:"created_at"`
    UpdatedAt           time.Time      `json:"updated_at"`
}

type SyncItem struct {
    SessionID string
    TenantID  string
    UserID    string
    Message   Message
    EnqueueAt time.Time
}

type SyncQueue struct {
    ch            chan *SyncItem
    batchSize     int
    flushInterval time.Duration
    dlq           chan *SyncItem
}

type SessionManager struct {
    lru       *lru.Cache
    cache     CacheStore
    store     SessionStore
    syncQueue *SyncQueue
    config    *SessionConfig
}

func (m *SessionManager) cacheKey(tenantID, userID, sessionID string) string
func (m *SessionManager) GetOrCreate(ctx context.Context, rc RequestContext) (*Session, error)
func (m *SessionManager) AddMessage(ctx context.Context, session *Session, msg Message) error
func (m *SessionManager) RecordUsage(ctx context.Context, session *Session, records []UsageRecord) error
func (m *SessionManager) Clone(session *Session) *Session
```

隔离规则：

- 所有会话加载和写入都以 `tenant_id + user_id + session_id` 为键
- 本地 LRU 和 Redis 缓存键格式为 `{tenant_id}:{user_id}:{session_id}`
- PG 中 `sessions` 和 `session_messages` 使用同样的三元组隔离
- `UsageRecord` 与 `SessionMeta` 一并按相同三元组隔离
- `RecordUsage` 用于持久化客户端显式上报的 used contexts / skills / tools，并同步更新 `SessionMeta`

### CompactProcessor 组件设计

CompactProcessor 的总体逻辑保持原设计不变：异步执行、分布式锁、原子性持久化、原始消息无损保留。

```go
type CompactCheckpoint struct {
    ID                   string    `json:"id"`
    SessionID            string    `json:"session_id"`
    TenantID             string    `json:"tenant_id"`
    UserID               string    `json:"user_id"`
    CommittedAt          time.Time `json:"committed_at"`
    SourceTurnStart      int       `json:"source_turn_start"`
    SourceTurnEnd        int       `json:"source_turn_end"`
    SummaryContent       string    `json:"summary_content"`
    ExtractedMemoryIDs   []string  `json:"extracted_memory_ids"`
    PromptTokensUsed     int       `json:"prompt_tokens_used"`
    CompletionTokensUsed int       `json:"completion_tokens_used"`
}

type CompactProcessor struct {
    llm         LLMClient
    session     SessionStore
    profiles    ProfileStore
    cache       CacheStore
    vectorStore VectorStore
    tasks       TaskTracker
    hooks       *HookManager
    config      *CompactConfig
}

func (p *CompactProcessor) EvaluateAndTrigger(ctx context.Context, rc RequestContext, session *Session) (bool, error)
func (p *CompactProcessor) executeCompact(ctx context.Context, rc RequestContext, snapshot *Session) error
```

Compact 执行补充：

- 在生成会话摘要和提取记忆后，CompactProcessor 进一步调用画像提取逻辑，生成合并后的 `UserProfile`
- 画像更新按 `tenant_id + user_id` upsert，不与单次会话绑定
- 画像提取失败只记录错误日志，不回滚已完成的消息摘要和记忆写入
- 当 Compact 以后台模式触发时，CompactProcessor 先在 TaskTracker 中创建 `compact` 任务，再驱动状态从 `pending -> running -> completed/failed`

### RetrievalEngine 组件设计

```go
type RetrievalEngine struct {
    vectorStore VectorStore
    embedding   EmbeddingProvider
    config      *RetrievalConfig
}

func (r *RetrievalEngine) SemanticSearch(ctx context.Context, rc RequestContext, query string, budget int) ([]ContentBlock, error)
func (r *RetrievalEngine) PatternSearch(ctx context.Context, rc RequestContext, pattern string, mode string, budget int) ([]ContentBlock, error)
func (r *RetrievalEngine) BatchLoadContent(ctx context.Context, ids []string, level ContentLevel) ([]ContentBlock, error)
```

检索策略：

- 记忆与用户归档数据只按 `tenant_id + user_id` 过滤
- 会话历史只按 `tenant_id + user_id + session_id` 加载
- Skill 不参与租户过滤，使用全局启用 Skill 集
- PatternSearch 的 v1 搜索范围限定为：`memory_facts`、`compact_checkpoints.summary`、`user_profiles.summary` 和当前会话消息
- PatternSearch 不搜索文件系统，也不将 Skill `body` 纳入结果集
- 检索后统一做去重、排序和单条截断

### ToolRegistry 与 HookManager 组件设计

```go
type ToolRegistry struct {
    tools map[string]Tool
    mu    sync.RWMutex
}

func (r *ToolRegistry) Register(tool Tool) error
func (r *ToolRegistry) Unregister(name string)
func (r *ToolRegistry) Get(name string) (Tool, bool)
func (r *ToolRegistry) Execute(ctx context.Context, rc RequestContext, name string, params map[string]interface{}) (string, error)
func (r *ToolRegistry) ListDefinitions() []map[string]interface{}

type HookManager struct {
    hooks map[HookEvent][]Hook
    mu    sync.RWMutex
}

func (m *HookManager) Register(hook Hook)
func (m *HookManager) Trigger(ctx context.Context, hookCtx HookContext) error
```

Skill 工具绑定采用常见的“Skill 声明工具元数据，ToolRegistry 统一调度执行”模式：

```go
type SkillToolBinding struct {
    Name        string                 `json:"name" yaml:"name"`
    Description string                 `json:"description" yaml:"description"`
    InputSchema map[string]interface{} `json:"input_schema" yaml:"input_schema"`
    Binding     string                 `json:"binding" yaml:"binding"` // command:<name> / mcp:<server>:<tool> / builtin:<name>
}
```

设计决策：

- Skill 中声明的工具在 Skill 启用时注册，在 Skill 停用时注销
- ToolRegistry 对外只暴露统一 Tool 接口，不区分工具来源
- Skill `body` 的按需加载只影响上下文注入，不影响已启用 Skill 工具的注册状态
- 若 Skill 未声明 `tools[]`，视为纯上下文 Skill，不影响 ToolRegistry

### TaskTracker 组件设计

TaskTracker 用于统一追踪后台任务，吸收 OpenViking 的 `accepted + task_id` 语义，但保持当前中间件架构不变。

```go
type DefaultTaskTracker struct {
    cache CacheStore
    store *pgxpool.Pool
}
```

设计决策：

- 长耗时后台任务统一通过 TaskTracker 注册和查询
- v1 任务类型至少包含 `compact` 和 `skill_import`
- 任务状态严格单调：`pending -> running -> completed/failed`
- `GET /api/v1/tasks/:task_id` 返回 `TaskRecord`
- `GET /api/v1/observer/queue` 返回异步队列积压、失败重试和最近错误摘要

### Auth 组件设计

认证设计分为两套机制，职责明确分离。

#### 服务接入认证

```go
type APIKeyRecord struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    KeyHash   string    `json:"-"`
    KeyPrefix string    `json:"key_prefix"`
    Enabled   bool      `json:"enabled"`
    CreatedAt time.Time `json:"created_at"`
}

type APIKeyManager struct {
    store *pgxpool.Pool
    cache CacheStore
    keys  sync.Map // key_hash -> *APIKeyRecord
}

func (m *APIKeyManager) Create(ctx context.Context, name string) (fullKey string, err error)
func (m *APIKeyManager) Verify(ctx context.Context, apiKey string) (bool, error)
func (m *APIKeyManager) Revoke(ctx context.Context, keyID string) error
func (m *APIKeyManager) List(ctx context.Context) ([]APIKeyRecord, error)
```

HTTP 服务认证中间件：

```go
func ServiceAuthMiddleware(keyMgr *APIKeyManager) gin.HandlerFunc {
    return func(c *gin.Context) {
        // 1. 开发模式 -> 允许跳过服务认证
        // 2. 从 X-API-Key 提取并校验
        // 3. 从 X-Tenant-ID / X-User-ID 提取 RequestContext
        // 4. session_id 由 handler 从请求体解析
        // 5. 校验失败 -> 401
    }
}
```

参数约定：

```text
HTTP:
  X-API-Key:  ctx_xxxxxxxxxxxx
  X-Tenant-ID: acme
  X-User-ID:   user_123

非 HTTP:
  api_key:    ctx_xxxxxxxxxxxx
  tenant_id:  acme
  user_id:    user_123
  session_id: sess_abc
```

#### 管理员认证

管理员认证只用于 CLI 和管理 API。

```go
type AdminUser struct {
    ID           string    `json:"id"`
    Username     string    `json:"username"`
    PasswordHash string    `json:"-"`
    CreatedAt    time.Time `json:"created_at"`
    UpdatedAt    time.Time `json:"updated_at"`
}

type AdminSession struct {
    Token     string    `json:"token"`
    UserID    string    `json:"user_id"`
    Username  string    `json:"username"`
    ExpiresAt time.Time `json:"expires_at"`
}

type AdminAuth struct {
    store *pgxpool.Pool
    cache CacheStore // Redis: admin_session:{token}
}

func (a *AdminAuth) BootstrapDefaultAdmin(ctx context.Context, username, password string) error
func (a *AdminAuth) CreateAdmin(ctx context.Context, username, password string) error
func (a *AdminAuth) Login(ctx context.Context, username, password string) (*AdminSession, error)
func (a *AdminAuth) VerifySession(ctx context.Context, token string) (*AdminSession, error)
func (a *AdminAuth) UpdatePassword(ctx context.Context, username, password string) error
func (a *AdminAuth) HasAdmin(ctx context.Context) (bool, error)
```

设计决策：

- 默认管理员账号密码从服务配置读取
- 只有当 `admin_users` 表为空时，`BootstrapDefaultAdmin` 才会生效
- `/api/v1/auth/setup` 仅在 `HasAdmin == false` 时允许调用，否则返回 409 Conflict
- 登录成功后返回随机 `admin_session_token`
- `admin_session_token` 存储在 Redis，默认 TTL 24h
- `/api/v1/auth/verify` 返回当前管理员会话的 `user_id`、`username`、`expires_at`
- `/api/v1/admin/users` 的 v1 能力为 `list`、`create`、`update-password`、`disable`
- v1 不支持硬删除管理员；不得禁用最后一个可用管理员账号
- CLI 自动登录使用配置中的管理员账号密码调用 `/api/v1/auth/login`
- CLI 本地不保存服务 API Key 作为管理员凭证

管理 API 认证约定：

```text
Authorization: Bearer <admin_session_token>
```

### Agent 接入层设计

```go
type Engine interface {
    Assemble(ctx context.Context, rc RequestContext, req AssembleRequest) (*AssembleResponse, error)
    Ingest(ctx context.Context, rc RequestContext, req IngestRequest) (*IngestResponse, error)
    SearchMemory(ctx context.Context, rc RequestContext, query string, limit int) ([]SearchResult, error)
    StoreMemory(ctx context.Context, rc RequestContext, content string, metadata map[string]string) error
    ForgetMemory(ctx context.Context, rc RequestContext, memoryID string) error
    GetSessionSummary(ctx context.Context, rc RequestContext) (string, error)
    ExecuteTool(ctx context.Context, rc RequestContext, toolName string, params map[string]interface{}) (string, error)
}

type IngestRequest struct {
    SessionID    string         `json:"session_id"`
    Messages     []Message      `json:"messages"`
    UsedContexts []string       `json:"used_contexts,omitempty"`
    UsedSkills   []string       `json:"used_skills,omitempty"`
    ToolCalls    []UsageRecord  `json:"tool_calls,omitempty"`
    Telemetry    *TelemetryOption `json:"telemetry,omitempty"`
}

type IngestResponse struct {
    Written          int  `json:"written"`
    CompactTriggered bool `json:"compact_triggered"`
    CompactTaskID    string `json:"compact_task_id,omitempty"`
    Telemetry        *OperationTelemetry `json:"telemetry,omitempty"`
}

type MemorySearchRequest struct {
    Query     string           `json:"query"`
    Limit     int              `json:"limit,omitempty"`
    Telemetry *TelemetryOption `json:"telemetry,omitempty"`
}

type MemoryStoreRequest struct {
    Content  string            `json:"content"`
    Category string            `json:"category,omitempty"`
    Metadata map[string]string `json:"metadata,omitempty"`
    Telemetry *TelemetryOption `json:"telemetry,omitempty"`
}

type ToolExecuteRequest struct {
    SessionID string                 `json:"session_id"`
    ToolName  string                 `json:"tool_name"`
    Params    map[string]interface{} `json:"params"`
    Telemetry *TelemetryOption       `json:"telemetry,omitempty"`
}

type AuthVerifyResponse struct {
    UserID    string    `json:"user_id"`
    Username  string    `json:"username"`
    ExpiresAt time.Time `json:"expires_at"`
}

type TaskGetResponse struct {
    Task *TaskRecord `json:"task"`
}

type WebhookEvent struct {
    ID        string                 `json:"id"`
    Type      string                 `json:"type"`
    TenantID  string                 `json:"tenant_id"`
    UserID    string                 `json:"user_id"`
    SessionID string                 `json:"session_id"`
    Payload   map[string]interface{} `json:"payload"`
    OccurredAt time.Time             `json:"occurred_at"`
}
```

业务 API wire shape 约定：

- `AssembleRequest` 最小字段为 `session_id`、`query`，`token_budget` 可选
- `IngestRequest.Messages` 按追加顺序写入，允许的 `role` 为 `system`、`user`、`assistant`、`tool`
- `IngestRequest` 可选接收 `used_contexts`、`used_skills` 和 `tool_calls`，用于记录本轮真实使用痕迹
- `MemorySearchRequest.limit` 默认值为 `10`
- `MemoryStoreRequest.category` 默认为 `fact`
- `ToolExecuteRequest.params` 必须满足目标工具 `Schema()`
- `telemetry` 在 HTTP JSON 请求中支持 `true` 或 `{ "summary": true }` 两种等价开启方式
- 显式请求 telemetry 时，响应体返回 `telemetry.id` 和 `telemetry.summary`
- 所有 HTTP 响应附带 `X-Request-Id` 和 `X-Process-Time`

接入方式参数传递对比：

```text
HTTP API:
  X-API-Key: 请求头
  X-Tenant-ID: 请求头
  X-User-ID: 请求头
  session_id: 请求体

Go SDK:
  api_key: 初始化参数
  tenant_id, user_id, session_id: 方法参数

MCP Server:
  api_key: 启动参数 (--api-key)
  tenant_id, user_id, session_id: 工具 inputSchema 参数
```

Webhook 说明：

- Webhook 不是主动调用 ContextOS 的接入方式
- Webhook 使用同一套核心事件模型，但不参与请求式行为一致性校验
- Webhook v1 默认面向内网受控环境，不强制要求签名校验
- 每次投递都携带 `X-ContextOS-Event` 和 `X-ContextOS-Delivery-ID` 头，POST body 即 `WebhookEvent`

### Operation Telemetry 与 Observer 设计

吸收 OpenViking 的做法，ContextOS 将“请求级 telemetry”和“系统级 observer”拆开：

- telemetry 用于解释某一次请求内部发生了什么
- observer 用于查看系统整体和后台队列状态

telemetry 设计决策：

- 仅在请求显式携带 `telemetry=true` 或 `telemetry.summary=true` 时返回
- `telemetry.summary` 只返回紧凑摘要，不返回完整日志明细
- 适用分组包括：
  - `operation` / `status` / `duration_ms`
  - `tokens`
  - `vector`
  - `skill`
  - `compact`
  - `profile`
  - `errors`
- 不适用的分组直接省略，不返回空对象

observer 设计决策：

- `GET /api/v1/observer/system` 返回核心组件健康状态、最近错误和是否可服务
- `GET /api/v1/observer/queue` 返回同步队列、Compact 队列、异步任务数和失败重试统计
- `/healthz` 仅用于 liveness，保持匿名可访问
- `/readyz` 用于 readiness，检查 PostgreSQL、Redis、模型配置和关键依赖状态

## 数据模型

### 核心结构体

```go
type MemoryFact struct {
    ID              string            `json:"id"`
    TenantID        string            `json:"tenant_id"`
    UserID          string            `json:"user_id"`
    Content         string            `json:"content"`
    Category        string            `json:"category"`
    URI             string            `json:"uri"`
    Vector          []float32         `json:"vector,omitempty"`
    CreatedAt       time.Time         `json:"created_at"`
    SourceSessionID string            `json:"source_session_id"`
    SourceTurnRange [2]int            `json:"source_turn_range"`
    Metadata        map[string]string `json:"metadata,omitempty"`
}

type UserProfile struct {
    TenantID        string            `json:"tenant_id"`
    UserID          string            `json:"user_id"`
    Summary         string            `json:"summary"`
    Interests       []string          `json:"interests"`
    Preferences     []string          `json:"preferences"`
    Goals           []string          `json:"goals"`
    Constraints     []string          `json:"constraints"`
    Metadata        map[string]string `json:"metadata,omitempty"`
    SourceSessionID string            `json:"source_session_id"`
    UpdatedAt       time.Time         `json:"updated_at"`
}

type ModelType string

const (
    ModelTypeLLM       ModelType = "llm"
    ModelTypeEmbedding ModelType = "embedding"
)

type ModelProvider struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    APIBase   string    `json:"api_base"`
    APIKey    string    `json:"api_key"`
    Enabled   bool      `json:"enabled"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type ModelConfig struct {
    ID         string    `json:"id"`
    Name       string    `json:"name"`
    ProviderID string    `json:"provider_id"`
    ModelID    string    `json:"model_id"`
    Type       ModelType `json:"type"`
    Dimension  int       `json:"dimension"`
    IsDefault  bool      `json:"is_default"`
    Enabled    bool      `json:"enabled"`
    CreatedAt  time.Time `json:"created_at"`
    UpdatedAt  time.Time `json:"updated_at"`
}
```

### Skill 动态管理设计

Skill 设计保留原有“动态启停 + ToolRegistry 联动 + Redis Pub/Sub 刷新”逻辑，只将存储真相切换到数据库并去掉租户隔离。

```go
type SkillStatus string

const (
    SkillEnabled  SkillStatus = "enabled"
    SkillDisabled SkillStatus = "disabled"
)

type SkillDocument struct {
    Name        string `json:"name" yaml:"name"`
    Description string `json:"description" yaml:"description"`
    Body        string `json:"body" yaml:"body"`
    Tools       []SkillToolBinding `json:"tools,omitempty" yaml:"tools,omitempty"`
}

type SkillMeta struct {
    ID          string      `json:"id"`
    Name        string      `json:"name"`
    Description string      `json:"description"`
    Body        string      `json:"body"`
    Status      SkillStatus `json:"status"`
    Tools       []SkillToolBinding `json:"tools"`
    CreatedAt   time.Time   `json:"created_at"`
    UpdatedAt   time.Time   `json:"updated_at"`
}

type SkillManager struct {
    store  *pgxpool.Pool
    cache  CacheStore
    tools  *ToolRegistry
    skills sync.Map // id -> *SkillMeta
}

func (m *SkillManager) Add(ctx context.Context, doc SkillDocument) (*SkillMeta, error)
func (m *SkillManager) Remove(ctx context.Context, id string) error
func (m *SkillManager) Enable(ctx context.Context, id string) error
func (m *SkillManager) Disable(ctx context.Context, id string) error
func (m *SkillManager) List(ctx context.Context) ([]SkillMeta, error)
func (m *SkillManager) Info(ctx context.Context, id string) (*SkillMeta, error)
func (m *SkillManager) LoadCatalog(ctx context.Context) ([]SkillMeta, error)
func (m *SkillManager) LoadBody(ctx context.Context, id string) (string, error)
func (m *SkillManager) onStatusChange(ctx context.Context, skill *SkillMeta)
```

Skill 加载规则：

- `LoadCatalog` 只返回启用 Skill 的 `id`、`name`、`description`、`status`
- `LoadBody` 单独读取正文
- ContextBuilder 先通过 `LoadCatalog` 构建 Skill L0 内容块
- 命中后再通过 `LoadBody` 加载正文
- 当 `LoadCatalog` 结果为空时，`LoadBody` 不应被调用，Skill 路径直接短路返回
- Skill catalog 加载结果应缓存到内存，减少重复读取
- Skill 导入或更新时应预计算并存储 `name + description` 的 `catalog_embedding`
- 若 Skill 被启用、停用或修改，Redis Pub/Sub 事件触发本地 catalog 缓存失效
- HTTP Skill 导入支持两种输入：
  - 结构化 `SkillDocument`
  - `temp_file_id` 指向的临时上传文件或压缩包
- 服务端不接受宿主机本地路径作为导入参数；CLI/SDK 负责本地文件读取和上传中转
- 当导入请求使用 `wait=false` 时，SkillManager 可创建 `skill_import` 任务并异步完成预处理与向量预计算
- 临时上传文件存放在服务端受控临时目录，使用 `temp_file_id` 索引，并通过 TTL 定期清理

CLI 示例保持原有交互风格，但导入格式改为统一 Skill 文档：

```text
ctx> /skill add ./review-skill.yaml
ctx> /skill list
ctx> /skill info code-review
ctx> /skill enable code-review
ctx> /skill disable code-review
ctx> /skill remove code-review
```

统一 Skill 文档格式示例：

```yaml
name: code-review
description: 用于代码审查时输出风险、回归和测试缺口
body: |
  你是一个严格的代码审查助手。
  输出顺序必须是 findings -> assumptions -> summary。
tools:
  - name: repo_grep
    description: 在代码仓库中按关键词搜索实现
    input_schema:
      type: object
      properties:
        query:
          type: string
      required: [query]
    binding: builtin:repo_grep
```

### PostgreSQL 数据库 Schema

```sql
CREATE TABLE sessions (
    id           VARCHAR(64) NOT NULL,
    tenant_id    VARCHAR(64) NOT NULL,
    user_id      VARCHAR(64) NOT NULL,
    metadata     JSONB       NOT NULL DEFAULT '{}',
    commit_count INT         NOT NULL DEFAULT 0,
    contexts_used INT        NOT NULL DEFAULT 0,
    skills_used  INT         NOT NULL DEFAULT 0,
    tools_used   INT         NOT NULL DEFAULT 0,
    llm_token_usage JSONB    NOT NULL DEFAULT '{}',
    embedding_token_usage JSONB NOT NULL DEFAULT '{}',
    max_messages INT         NOT NULL DEFAULT 50,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, user_id, id)
);
CREATE INDEX idx_sessions_user ON sessions(tenant_id, user_id);

CREATE TABLE session_messages (
    tenant_id  VARCHAR(64) NOT NULL,
    user_id    VARCHAR(64) NOT NULL,
    session_id VARCHAR(64) NOT NULL,
    seq        INT         NOT NULL,
    role       VARCHAR(16) NOT NULL,
    content    TEXT        NOT NULL,
    metadata   JSONB       NOT NULL DEFAULT '{}',
    tool_calls JSONB       NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, user_id, session_id, seq),
    FOREIGN KEY (tenant_id, user_id, session_id)
        REFERENCES sessions(tenant_id, user_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_session_messages_recent
    ON session_messages(tenant_id, user_id, session_id, seq DESC);

CREATE TABLE compact_checkpoints (
    id                     VARCHAR(64) PRIMARY KEY,
    session_id             VARCHAR(64) NOT NULL,
    tenant_id              VARCHAR(64) NOT NULL,
    user_id                VARCHAR(64) NOT NULL,
    committed_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    source_turn_start      INT         NOT NULL,
    source_turn_end        INT         NOT NULL,
    summary_content        TEXT        NOT NULL,
    extracted_memory_ids   JSONB       NOT NULL DEFAULT '[]',
    prompt_tokens_used     INT         NOT NULL DEFAULT 0,
    completion_tokens_used INT         NOT NULL DEFAULT 0,
    FOREIGN KEY (tenant_id, user_id, session_id)
        REFERENCES sessions(tenant_id, user_id, id) ON DELETE CASCADE
);

CREATE TABLE session_usage_records (
    id           VARCHAR(64) PRIMARY KEY,
    tenant_id    VARCHAR(64) NOT NULL,
    user_id      VARCHAR(64) NOT NULL,
    session_id   VARCHAR(64) NOT NULL,
    uri          VARCHAR(512) NOT NULL DEFAULT '',
    skill_name   VARCHAR(128) NOT NULL DEFAULT '',
    tool_name    VARCHAR(128) NOT NULL DEFAULT '',
    input_summary TEXT        NOT NULL DEFAULT '',
    output_summary TEXT       NOT NULL DEFAULT '',
    success      BOOLEAN      NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_session_usage_records_session
    ON session_usage_records(tenant_id, user_id, session_id, created_at DESC);
CREATE INDEX idx_compact_session ON compact_checkpoints(tenant_id, user_id, session_id);

CREATE TABLE memory_facts (
    id                VARCHAR(64) PRIMARY KEY,
    tenant_id         VARCHAR(64) NOT NULL,
    user_id           VARCHAR(64) NOT NULL,
    content           TEXT        NOT NULL,
    category          VARCHAR(32) NOT NULL,
    uri               VARCHAR(512),
    source_session_id VARCHAR(64),
    source_turn_start INT,
    source_turn_end   INT,
    metadata          JSONB       NOT NULL DEFAULT '{}',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_memory_tenant ON memory_facts(tenant_id, user_id);

CREATE TABLE audit_logs (
    id          BIGSERIAL   PRIMARY KEY,
    ts          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id   VARCHAR(64) NOT NULL,
    user_id     VARCHAR(64) NOT NULL,
    action      VARCHAR(64) NOT NULL,
    target_type VARCHAR(32) NOT NULL,
    target_id   VARCHAR(128),
    detail      JSONB       NOT NULL DEFAULT '{}',
    trace_id    VARCHAR(64)
);
CREATE INDEX idx_audit_ts ON audit_logs(ts);
CREATE INDEX idx_audit_tenant ON audit_logs(tenant_id, user_id);

CREATE TABLE token_usage (
    id                BIGSERIAL   PRIMARY KEY,
    ts                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tenant_id         VARCHAR(64) NOT NULL,
    user_id           VARCHAR(64) NOT NULL,
    call_type         VARCHAR(32) NOT NULL,
    model             VARCHAR(128) NOT NULL,
    prompt_tokens     INT         NOT NULL DEFAULT 0,
    completion_tokens INT         NOT NULL DEFAULT 0,
    total_tokens      INT         NOT NULL DEFAULT 0,
    session_id        VARCHAR(64),
    trace_id          VARCHAR(64)
);
CREATE INDEX idx_token_usage_ts ON token_usage(ts);
CREATE INDEX idx_token_usage_tenant ON token_usage(tenant_id, user_id);

CREATE TABLE model_providers (
    id         VARCHAR(64) PRIMARY KEY,
    name       VARCHAR(128) NOT NULL UNIQUE,
    api_base   VARCHAR(512) NOT NULL,
    api_key    VARCHAR(512) NOT NULL,
    enabled    BOOLEAN      NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE models (
    id         VARCHAR(64) PRIMARY KEY,
    name       VARCHAR(128) NOT NULL UNIQUE,
    provider_id VARCHAR(64) NOT NULL REFERENCES model_providers(id),
    model_id   VARCHAR(256) NOT NULL,
    type       VARCHAR(16)  NOT NULL,
    dimension  INT          NOT NULL DEFAULT 0,
    is_default BOOLEAN      NOT NULL DEFAULT false,
    enabled    BOOLEAN      NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_models_type ON models(type);

CREATE TABLE skills (
    id          VARCHAR(64) PRIMARY KEY,
    name        VARCHAR(128) NOT NULL UNIQUE,
    description TEXT         NOT NULL DEFAULT '',
    body        TEXT         NOT NULL,
    status      VARCHAR(16)  NOT NULL DEFAULT 'enabled',
    tool_bindings JSONB      NOT NULL DEFAULT '[]',
    catalog_embedding vector,  -- 维度由运行时 EmbeddingProvider.Dimension() 决定
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE user_profiles (
    tenant_id         VARCHAR(64)  NOT NULL,
    user_id           VARCHAR(64)  NOT NULL,
    summary           TEXT         NOT NULL,
    interests         JSONB        NOT NULL DEFAULT '[]',
    preferences       JSONB        NOT NULL DEFAULT '[]',
    goals             JSONB        NOT NULL DEFAULT '[]',
    constraints       JSONB        NOT NULL DEFAULT '[]',
    metadata          JSONB        NOT NULL DEFAULT '{}',
    source_session_id VARCHAR(64),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    PRIMARY KEY (tenant_id, user_id)
);

CREATE TABLE api_keys (
    id         VARCHAR(64) PRIMARY KEY,
    name       VARCHAR(128) NOT NULL,
    key_hash   VARCHAR(128) NOT NULL UNIQUE,
    key_prefix VARCHAR(16)  NOT NULL,
    enabled    BOOLEAN      NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE admin_users (
    id            VARCHAR(64) PRIMARY KEY,
    username      VARCHAR(128) NOT NULL UNIQUE,
    password_hash VARCHAR(256) NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE TABLE webhook_subscriptions (
    id         VARCHAR(64) PRIMARY KEY,
    tenant_id  VARCHAR(64)  NOT NULL DEFAULT 'default',
    url        VARCHAR(1024) NOT NULL,
    events     JSONB        NOT NULL DEFAULT '[]',
    enabled    BOOLEAN      NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_webhook_tenant ON webhook_subscriptions(tenant_id);

CREATE TABLE tasks (
    id             VARCHAR(64) PRIMARY KEY,
    type           VARCHAR(64)  NOT NULL,
    status         VARCHAR(16)  NOT NULL,
    trace_id       VARCHAR(64)  NOT NULL DEFAULT '',
    result_summary JSONB        NOT NULL DEFAULT '{}',
    error          TEXT         NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    started_at     TIMESTAMPTZ,
    finished_at    TIMESTAMPTZ
);
CREATE INDEX idx_tasks_status ON tasks(status, created_at DESC);
```

### 向量数据库 Schema（pgvector 示例）

向量列维度不在迁移 SQL 中硬编码，而是在 `VectorStore.Init()` 运行时根据 `EmbeddingProvider.Dimension()` 动态创建。迁移 SQL 仅负责创建不含 `embedding` 列的基础表结构；`VectorStore.Init()` 在首次启动时通过 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS embedding vector(N)` 补充向量列并创建索引。`skills.catalog_embedding` 列同理，由 SkillManager 初始化时根据当前 embedding 维度动态添加。

这样做的好处是：不同部署环境可以使用不同维度的 embedding 模型（如 768、1024、1536、3072），无需修改迁移文件。

基础迁移 SQL（`005_vector_extension.up.sql`）：

```sql
-- 仅创建 pgvector 扩展和基础表结构，不指定向量维度
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE IF NOT EXISTS vector_items (
    id         VARCHAR(64) PRIMARY KEY,
    tenant_id  VARCHAR(64) NOT NULL,
    user_id    VARCHAR(64) NOT NULL,
    content    TEXT        NOT NULL,
    uri        VARCHAR(512),
    metadata   JSONB       NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_vector_tenant ON vector_items(tenant_id, user_id);
-- embedding 列和 ivfflat 索引由 VectorStore.Init() 动态创建
```

运行时 `VectorStore.Init()` 执行的动态 DDL 示例（以 dimension=1536 为例）：

```sql
ALTER TABLE vector_items ADD COLUMN IF NOT EXISTS embedding vector(1536);
CREATE INDEX IF NOT EXISTS idx_vector_embedding
    ON vector_items USING ivfflat (embedding vector_cosine_ops);

ALTER TABLE skills ADD COLUMN IF NOT EXISTS catalog_embedding vector(1536);
```

## CLI 命令设计

两种运行模式保持不变：

```bash
ctx serve
ctx migrate up
ctx version
ctx doctor
ctx
```

配置文件示例（同一份配置供 `ctx serve` 和 `ctx` 复用）：

```yaml
server:
  url: http://localhost:8080
  port: 8080
  development_mode: false

admin:
  bootstrap_username: admin
  bootstrap_password: change-me
  username: admin
  password: change-me

redis:
  mode: standalone          # standalone / sentinel / cluster
  addr: localhost:6379
  password: ""
  db: 0
  # sentinel 模式
  # sentinel_addrs: ["sentinel1:26379", "sentinel2:26379"]
  # sentinel_master: mymaster
  # sentinel_password: ""
  # cluster 模式
  # cluster_addrs: ["node1:6379", "node2:6379", "node3:6379"]
  pool_size: 10

postgres:
  dsn: postgres://contextos:password@localhost:5432/contextos?sslmode=disable
```

CLI 认证流程：

1. 读取配置中的 `admin.username` 和 `admin.password`
2. 调用 `/api/v1/auth/login`
3. 登录成功后在本地内存中保存 `admin_session_token`
4. REPL 生命周期内用该 token 调管理 API
5. `/logout` 清除本地登录态

交互式 REPL 命令：

```text
/admin    管理员管理（add / passwd / list）
/provider 供应商管理
/model    模型管理
/skill    Skill 管理
/session  会话管理
/memory   记忆管理
/search   快捷搜索
/apikey   API Key 管理
/migrate  数据库迁移
/logs     日志查询
/status   系统状态
/help     帮助
/logout   退出登录
/exit     退出
```

Skill CLI 示例：

```text
ctx> /skill add ./review-skill.yaml
ctx> /skill info code-review
ctx> /skill disable code-review
ctx> /skill enable code-review
```

诊断命令示例：

```text
ctx doctor
  ✓ 配置文件可读取
  ✓ PostgreSQL 连接正常
  ✓ Redis 连接正常
  ! embedding 模型未配置默认值
```

API Key CLI 示例：

```text
ctx> /apikey create
  名称: staging-agent
  ✓ API Key 已颁发
    Key: ctx_e82d4e0f...  <- 仅显示一次
```

管理员 CLI 示例：

```text
ctx> /admin add
  账号: ops-admin
  密码: ********
  ✓ 管理员 ops-admin 已创建
```

## 数据库自动迁移设计

迁移机制、启动流程和 advisory lock 思路保持原设计不变。迁移内容只随本次修订做如下调整：

- `skills` 表改为全局共享表，不包含 `tenant_id`
- 保留 `api_keys` 和 `admin_users`
- 不保留旧的单一 root key 配置模型，服务认证统一走 `api_keys`
- 向量列维度不在迁移 SQL 中硬编码；`005_vector_extension` 仅创建 pgvector 扩展和基础表结构，向量列和索引由 `VectorStore.Init()` 根据 `EmbeddingProvider.Dimension()` 动态创建

```go
type Migrator struct {
    db     *pgxpool.Pool
    config *MigrateConfig
}

type MigrateConfig struct {
    AutoMigrate bool `json:"auto_migrate" yaml:"auto_migrate" default:"true"`
}

func (m *Migrator) Run(ctx context.Context) error
func (m *Migrator) Status(ctx context.Context) ([]MigrationRecord, error)
func (m *Migrator) Rollback(ctx context.Context) error
```

迁移文件顺序：

```text
001_init_schema.up.sql
002_auth_tables.up.sql
003_audit_tables.up.sql
004_model_tables.up.sql
005_vector_extension.up.sql
```

## 配置结构体

```go
type Config struct {
    Server    ServerConfig    `json:"server" yaml:"server"`
    Admin     AdminConfig     `json:"admin" yaml:"admin"`
    Redis     RedisConfig     `json:"redis" yaml:"redis"`
    Postgres  PostgresConfig  `json:"postgres" yaml:"postgres"`
    LLM       LLMConfig       `json:"llm" yaml:"llm"`
    Embedding EmbeddingConfig `json:"embedding" yaml:"embedding"`
    Vector    VectorConfig    `json:"vector" yaml:"vector"`
    Engine    EngineConfig    `json:"engine" yaml:"engine"`
    Log       LogConfig       `json:"log" yaml:"log"`
    Migrate   MigrateConfig   `json:"migrate" yaml:"migrate"`
}

type ServerConfig struct {
    URL             string `json:"url" yaml:"url"`
    Port            int    `json:"port" yaml:"port" default:"8080"`
    DevelopmentMode bool   `json:"development_mode" yaml:"development_mode" default:"false"`
}

type AdminConfig struct {
    BootstrapUsername string `json:"bootstrap_username" yaml:"bootstrap_username"`
    BootstrapPassword string `json:"bootstrap_password" yaml:"bootstrap_password"`
    Username          string `json:"username" yaml:"username"`
    Password          string `json:"password" yaml:"password"`
}

type RedisMode string

const (
    RedisModeStandalone RedisMode = "standalone"
    RedisModeSentinel   RedisMode = "sentinel"
    RedisModeCluster    RedisMode = "cluster"
)

type RedisConfig struct {
    Mode             RedisMode `json:"mode" yaml:"mode" default:"standalone"`
    // standalone 模式
    Addr             string    `json:"addr" yaml:"addr" default:"localhost:6379"`
    Password         string    `json:"password" yaml:"password"`
    DB               int       `json:"db" yaml:"db" default:"0"`
    // sentinel 模式
    SentinelAddrs    []string  `json:"sentinel_addrs" yaml:"sentinel_addrs"`
    SentinelMaster   string    `json:"sentinel_master" yaml:"sentinel_master"`
    SentinelPassword string    `json:"sentinel_password" yaml:"sentinel_password"`
    // cluster 模式
    ClusterAddrs     []string  `json:"cluster_addrs" yaml:"cluster_addrs"`
    // 通用
    PoolSize         int       `json:"pool_size" yaml:"pool_size" default:"10"`
    MaxRetries       int       `json:"max_retries" yaml:"max_retries" default:"3"`
}

type EngineConfig struct {
    TokenBudget           int     `json:"token_budget" yaml:"token_budget" default:"32000"`
    MaxMessages           int     `json:"max_messages" yaml:"max_messages" default:"50"`
    CompactBudgetRatio    float64 `json:"compact_budget_ratio" yaml:"compact_budget_ratio" default:"0.5"`
    CompactTokenThreshold int     `json:"compact_token_threshold" yaml:"compact_token_threshold" default:"16000"`
    CompactTurnThreshold  int     `json:"compact_turn_threshold" yaml:"compact_turn_threshold" default:"10"`
    CompactIntervalMin    int     `json:"compact_interval_min" yaml:"compact_interval_min" default:"15"`
    CompactTimeoutSec     int     `json:"compact_timeout_sec" yaml:"compact_timeout_sec" default:"120"`
    MaxConcurrentCompacts int     `json:"max_concurrent_compacts" yaml:"max_concurrent_compacts" default:"10"`
    RecentRawTurnCount    int     `json:"recent_raw_turn_count" yaml:"recent_raw_turn_count" default:"8"`
    RecallScoreThreshold  float64 `json:"recall_score_threshold" yaml:"recall_score_threshold" default:"0.7"`
    RecallMaxResults      int     `json:"recall_max_results" yaml:"recall_max_results" default:"10"`
    SyncQueueSize         int     `json:"sync_queue_size" yaml:"sync_queue_size" default:"10000"`
    SyncBatchSize         int     `json:"sync_batch_size" yaml:"sync_batch_size" default:"100"`
    SyncFlushIntervalMs   int     `json:"sync_flush_interval_ms" yaml:"sync_flush_interval_ms" default:"500"`
    LRUCacheTTLSec        int     `json:"lru_cache_ttl_sec" yaml:"lru_cache_ttl_sec" default:"5"`
    SlowQueryMs           int     `json:"slow_query_ms" yaml:"slow_query_ms" default:"300"`
}
```

## 错误处理

### 错误分类

| 错误类型 | HTTP 状态码 | 处理策略 |
|---------|------------|---------|
| 服务认证失败（无效 API Key） | 401 | 拒绝请求，记录审计日志 |
| 管理员认证失败（账号密码错误或 token 失效） | 401 | 拒绝管理操作 |
| 权限不足 | 403 | 拒绝请求，记录审计日志 |
| 资源不存在 | 404 | 返回错误，会话访问可自动创建 |
| 参数校验失败 | 400 | 返回详细错误信息 |
| Token 预算超限 | 200 | 降级处理，返回部分结果 |
| LLM API 调用失败 | 502 | 重试 3 次，失败后返回错误 |
| Embedding API 调用失败 | 502 | 重试 3 次，失败后跳过语义检索 |
| Redis 不可用 | 200/503 | 业务请求降级，管理态会话校验失败时返回 503 |
| PostgreSQL 不可用 | 503 | 服务不可用，`/readyz` 返回 false |
| VectorStore 不可用 | 200 | 跳过语义检索，仅返回其他上下文 |
| Compact 执行失败 | N/A | 释放锁并记录错误，不影响原始数据 |
| Webhook 投递失败 | N/A | 重试 3 次，最终失败记录错误日志 |

### 错误结构

```go
type AppError struct {
    Code    string `json:"code"`
    Message string `json:"message"`
    TraceID string `json:"trace_id"`
}
```

## 正确性属性

### Property 1: 渐进式组装优先级不变量

*For any* 一组带相关性评分的内容块和一个 Token 预算，当预算不足以将所有内容升级到完整层时，ContextBuilder 结果中高评分内容的 ContentLevel 应大于等于低评分内容的 ContentLevel，且总 Token 消耗不超过预算。

**Validates: Requirements 1.2, 1.3, 7.3**

### Property 2: 组装结果 Token 估算一致性

*For any* 上下文组装请求，AssembleResponse 的 EstimatedTokens 在存在非空内容时应大于 0，且不超过请求的 TokenBudget。

**Validates: Requirements 1.6**

### Property 3: 内容层级大小有序性

*For any* 内容块，其 L0 的 Token 数应小于等于 L1，L1 应小于等于 L2。

**Validates: Requirements 1.1**

### Property 4: 会话持久化往返一致性

*For any* 有效 Session，对 SessionStore 保存后再加载，应产生消息历史和元数据等价的 Session。

**Validates: Requirements 2.3, 2.6**

### Property 5: 会话消息历史上限

*For any* 配置了 max_messages 的会话，返回的消息数量不超过上限，且返回最近消息。

**Validates: Requirements 2.2**

### Property 6: 会话自动创建

*For any* 不存在的 session_id，调用 GetOrCreate 应返回有效新 Session，且消息列表为空。

**Validates: Requirements 2.4**

### Property 7: 会话克隆深拷贝

*For any* Session，对克隆体的修改不影响原始 Session。

**Validates: Requirements 2.5**

### Property 8: 会话三元组隔离

*For any* 两个不同的 `(tenant_id, user_id, session_id)` 三元组，通过 SessionManager 写入的消息互不可见。

**Validates: Requirements 5.3, 5.7, 5.8**

### Property 8.5: 会话使用痕迹聚合

*For any* ingest 请求中显式提供的 `used_contexts`、`used_skills` 和 `tool_calls`，SessionManager 持久化后 `SessionMeta` 中的聚合计数应与 `session_usage_records` 一致。

**Validates: Requirements 2.7, 2.8, 2.9**

### Property 9: Compact 触发条件正确性

*For any* 会话状态和 Compact 配置，EvaluateAndTrigger 应在任一阈值满足时返回 triggered=true。

**Validates: Requirements 3.1**

### Property 10: Compact 互斥执行

*For any* 会话，当已有一个 Compact 任务在执行时，再次触发 Compact 应被拒绝。

**Validates: Requirements 3.7, 9.3, 10.2**

### Property 11: Compact 无损保存

*For any* 执行 Compact 的会话，Compact 完成后原始消息仍可访问，原始消息本体未被删除或替换。

**Validates: Requirements 3.6**

### Property 12: Compact 非阻塞

*For any* 触发 Compact 的调用，EvaluateAndTrigger 应在 Compact 完成前返回。

**Validates: Requirements 3.5**

### Property 12.5: 用户画像合并持久化

*For any* 同一 `(tenant_id, user_id)` 的多轮对话，Compact 或 ingest 触发画像更新后，ProfileStore 中应保持单条画像记录，并保留稳定偏好与目标信息。

**Validates: Requirements 3.4**

### Property 13: 配置序列化往返一致性

*For any* 有效 Config，格式化输出再解析后应产生等价结构体。

**Validates: Requirements 11.5, 11.6, 11.7**

### Property 14: 环境变量覆盖配置

*For any* 配置参数和对应的 `CONTEXTOS_` 环境变量，当环境变量被设置时，最终配置值应等于环境变量值。

**Validates: Requirements 11.2**

### Property 15: 向量存储往返一致性

*For any* 有效 VectorItem，执行 Upsert 后再以相同向量和过滤条件执行 Search，结果集应包含该记录。

**Validates: Requirements 6.5**

### Property 16: 租户用户过滤隔离

*For any* 向量搜索查询，当 Filter 指定了 `tenant_id` 和 `user_id` 时，返回结果不包含其他租户或用户的数据。

**Validates: Requirements 5.2, 5.4, 6.4, 7.1**

### Property 17: API Key 校验正确性

*For any* 已注册的 API Key，Verify 应返回 true；对未注册或已吊销 Key 应返回 false。

**Validates: Requirements 19.1, 19.4**

### Property 18: 无效 API Key 拒绝

*For any* 未注册或已吊销 API Key，在生产模式下的 HTTP 请求应返回 401。

**Validates: Requirements 13.3, 19.6**

### Property 19: 默认管理员初始化幂等

*For any* 启动配置中的默认管理员账号密码，当系统中无管理员时 BootstrapDefaultAdmin 创建一个管理员；重复执行不会创建重复账号。

**Validates: Requirements 4.3**

### Property 20: 管理员登录正确性

*For any* 已存在的管理员账号，正确账号密码登录成功，错误账号密码登录失败。

**Validates: Requirements 4.2, 4.8, 4.9**

### Property 21: 工具注册/注销往返

*For any* Tool，注册后通过 Get(name) 应能找到该工具，注销后应找不到。

**Validates: Requirements 8.2**

### Property 22: 钩子事件触发

*For any* 已注册 Hook 和匹配的 HookEvent，触发该事件时 Hook 的 Execute 方法应被调用。

**Validates: Requirements 8.4, 8.5**

### Property 23: 全局 Skill 持久化往返

*For any* 合法的 SkillDocument，导入到数据库后再读取，应得到等价的 `name`、`description`、`body` 和 `tools[]`。

**Validates: Requirements 18.2, 18.3**

### Property 24: Skill 摘要先注入、正文按需加载

*For any* 上下文组装请求，默认注入的 Skill 内容只包含 `name + description`；仅当 Skill 被判定命中时才会加载 `body`。

**Validates: Requirements 1.5, 18.4**

### Property 24.5: 无 Skill 时跳过 Skill 路径

*For any* 上下文组装请求，当系统中不存在任何启用 Skill 时，ContextBuilder 应跳过 Skill catalog 加载后的匹配和正文加载逻辑，且正常返回不含 Skill 内容的结果而不报错。

**Validates: Requirements 1.8, 18.9**

### Property 25: 一致性哈希确定性

*For any* `session_id` 和固定节点集合，一致性哈希应始终映射到同一节点。

**Validates: Requirements 9.2**

### Property 26: 数据对账修复一致性

*For any* Redis 与 PostgreSQL 之间的不一致状态，执行对账后两者应达到一致。

**Validates: Requirements 9.7**

### Property 27: WriteRedisFirst 最终一致性

*For any* 写入操作，数据应立即在 Redis 中可读，并在重试后最终同步到 PostgreSQL。

**Validates: Requirements 10.1, 10.5**

### Property 28: 结构化日志格式

*For any* 日志事件，输出 JSON 应包含 `ts`、`level`、`msg`、`component` 字段且为合法 JSON。

**Validates: Requirements 12.2**

### Property 29: 日志级别过滤

*For any* 组件级别配置，低于配置级别的日志不应被输出。

**Validates: Requirements 12.4**

### Property 30: 审计日志路由与完整性

*For any* 审计操作，应在 `audit_logs` 表中产生一条记录，包含操作者、操作类型、目标资源和时间戳。

**Validates: Requirements 12.5, 12.6**

### Property 31: Token 审计持久化与查询

*For any* LLM 或 Embedding 调用，应在 `token_usage` 表中产生一条记录，并可按条件正确查询。

**Validates: Requirements 12.10, 12.11, 12.12**

### Property 31.5: Telemetry 按需返回

*For any* 支持 telemetry 的请求，默认不返回 `telemetry`；当显式开启后返回紧凑 `telemetry.id` 与 `telemetry.summary`，且不适用的分组应被省略。

**Validates: Requirements 12.14, 12.15, 13.10, 20.7**

### Property 32: 慢查询告警

*For any* 上下文组装操作，当耗时超过 `slow_query_ms` 阈值时，应产生一条包含详情的警告日志。

**Validates: Requirements 12.9**

### Property 33: API 响应追踪 ID

*For any* HTTP API 响应，应包含非空的 `X-Request-Id` 头。

**Validates: Requirements 13.9**

### Property 33.5: API 处理耗时头

*For any* HTTP API 响应，应包含非空且可解析的 `X-Process-Time` 头。

**Validates: Requirements 12.16**

### Property 34: 多接入方式行为一致性

*For any* 相同的 Assemble 或 Ingest 请求，通过 HTTP API、Go SDK 和 MCP 执行应产生相同结果。

**Validates: Requirements 14.6**

### Property 35: Webhook 事件投递

*For any* 已注册的 Webhook URL 和匹配事件类型，当事件发生时系统应向该 URL 发送 POST 请求。

**Validates: Requirements 14.4, 14.5**

### Property 35.5: 任务状态单调推进

*For any* 后台任务，TaskTracker 记录的状态只能按 `pending -> running -> completed/failed` 单调推进，不允许回退。

**Validates: Requirements 20.1, 20.2, 20.3**

### Property 36: CLI 输出格式正确性

*For any* 数据集，当输出格式为 json 时，CLI 输出应为可解析 JSON；当输出格式为 table 时，输出应包含列标题行。

**Validates: Requirements 4.12**

### Property 37: 模式检索匹配正确性

*For any* 关键词、正则或 glob 模式，PatternSearch 返回的所有结果应匹配给定模式。

**Validates: Requirements 7.2**

### Property 38: 迁移幂等性

*For any* 已执行过的迁移版本集合，重复执行 `Migrator.Run()` 不应产生错误，且 `schema_migrations` 中记录不变。

**Validates: Requirements 16.2**

### Property 39: 迁移并发互斥

*For any* 多个节点同时调用 `Migrator.Run()`，仅有一个节点实际执行迁移 SQL。

**Validates: Requirements 16.5**

## 测试策略

### 双轨测试方法

本项目采用单元测试 + 属性测试双轨方法：

- 单元测试：验证具体示例、边界条件和错误处理
- 属性测试：验证跨所有输入的通用正确性，使用 `pgregory.net/rapid`

### 属性测试配置

- 每个属性测试最少运行 100 次迭代
- 每个属性测试必须通过注释引用设计文档中的属性编号
- 标签格式：`Feature: context-engine-middleware, Property {number}: {property_text}`

### 属性测试覆盖

| 属性编号 | 测试文件 | 测试内容 |
|---------|---------|---------|
| Property 1 | `engine/context_builder_test.go` | 渐进式组装优先级不变量 |
| Property 2 | `engine/context_builder_test.go` | Token 估算不超预算 |
| Property 3 | `engine/content_level_test.go` | L0 <= L1 <= L2 Token 大小 |
| Property 4 | `store/session_store_test.go` | 会话持久化往返 |
| Property 8 | `engine/session_manager_test.go` | 会话三元组隔离 |
| Property 8.5 | `engine/session_manager_test.go` | 会话使用痕迹聚合 |
| Property 9 | `engine/compact_processor_test.go` | Compact 触发条件 |
| Property 10 | `engine/compact_processor_test.go` | Compact 互斥执行 |
| Property 12.5 | `engine/profile_store_test.go` | 用户画像合并持久化 |
| Property 13 | `config/config_test.go` | 配置序列化往返 |
| Property 14 | `config/config_test.go` | 环境变量覆盖 |
| Property 15 | `store/vector_store_test.go` | 向量存储往返 |
| Property 16 | `store/vector_store_test.go` | 租户用户过滤隔离 |
| Property 17 | `auth/apikey_test.go` | API Key 校验 |
| Property 20 | `auth/admin_test.go` | 管理员登录 |
| Property 23 | `engine/skill_manager_test.go` | Skill 持久化往返 |
| Property 24 | `engine/context_builder_test.go` | Skill 渐进式加载 |
| Property 24.5 | `engine/context_builder_test.go` | 无 Skill 时跳过 Skill 路径 |
| Property 28 | `log/structured_test.go` | 结构化日志格式 |
| Property 29 | `log/level_filter_test.go` | 日志级别过滤 |
| Property 30 | `log/audit_test.go` | 审计日志路由 |
| Property 31 | `log/token_audit_test.go` | Token 审计持久化 |
| Property 31.5 | `log/telemetry_test.go` | Telemetry 按需返回 |
| Property 33 | `api/http_test.go` | API 响应追踪 ID |
| Property 33.5 | `api/http_test.go` | API 处理耗时头 |
| Property 34 | `sdk/consistency_test.go` | 多接入方式一致性 |
| Property 35 | `webhook/notifier_test.go` | Webhook 事件投递 |
| Property 35.5 | `engine/task_tracker_test.go` | 任务状态单调推进 |
| Property 36 | `cli/output_test.go` | CLI 输出格式 |
| Property 38 | `migrate/migrator_test.go` | 迁移幂等性 |
| Property 39 | `migrate/migrator_test.go` | 迁移并发互斥 |

### 单元测试覆盖

- ContextBuilder：空会话组装、单来源组装、Skill 命中与未命中、Token 预算为 0 的边界
- SessionManager：并发读写、空消息列表、超大消息
- CompactProcessor：所有四种触发条件边界值、锁超时、LLM 失败回滚
- AdminAuth：默认管理员初始化、错误密码登录、修改密码后旧密码失效
- APIKeyManager：创建、校验、吊销、吊销后缓存刷新
- VectorStore：空结果搜索、维度不匹配、大批量 Upsert
- CLI：各子命令的参数解析、配置文件不存在时的默认行为
