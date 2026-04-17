# 需求文档：ContextOS — Go 上下文引擎中间件

## 简介

ContextOS 是一个基于 Go 语言的 AI Agent 上下文管理中间件，为上游 Agent 应用提供统一的上下文生命周期管理能力。它负责上下文组装、会话管理、上下文压缩、记忆存储、Skill 渐进式加载以及多种接入方式下的一致鉴权和隔离。

## 术语表

- **ContextEngine**：上下文引擎核心模块，负责上下文组装、渐进加载和 Token 预算管理
- **SessionManager**：会话管理器，负责会话的创建、持久化、克隆和生命周期管理
- **CompactProcessor**：上下文压缩处理器，负责摘要生成、记忆提取和异步压缩执行
- **RequestContext**：请求上下文结构体，携带 `tenant_id`、`user_id`、`session_id`
- **VectorStore**：向量存储抽象接口，支持 pgvector、Elasticsearch、Milvus 等后端
- **EmbeddingProvider**：嵌入向量提供者接口，负责文本向量化并声明向量维度
- **ToolRegistry**：工具注册中心，管理 Skill、Command、MCP 三类工具的动态注册与执行
- **HookSystem**：钩子系统，提供事件驱动的生命周期回调机制
- **ContentLevel**：内容层级枚举，L0（摘要/索引）、L1（概览）、L2（完整内容）
- **管理员认证**：CLI 和管理 API 使用的账号密码认证体系
- **服务接入认证**：外部 Agent 或服务访问 ContextOS 时使用的 API Key 认证体系
- **归档数据**：按 `tenant_id + user_id` 隔离的长期数据，如记忆、用户画像、压缩摘要
- **实时会话数据**：按 `tenant_id + user_id + session_id` 隔离的当前会话数据，如会话元数据、消息历史和会话缓存
- **Skill**：全局共享的上下文能力单元，最小结构为 `name`、`description`、`body`
- **Operation Telemetry**：请求级结构化执行摘要，按需返回耗时、Token、检索、Skill、Compact 等阶段信息
- **TaskTracker**：统一异步任务追踪器，记录后台任务的状态、结果摘要和错误信息

## 需求

### 需求 1：三层渐进式内容加载

**用户故事：** 作为 AI Agent 开发者，我希望上下文引擎支持三层渐进式内容加载，以便在有限的 Token 预算内最大化上下文信息密度。

#### 验收标准

1. THE ContextEngine SHALL 支持三个内容层级：L0（摘要/索引）、L1（概览）、L2（完整内容）
2. WHEN 进行上下文组装时，THE ContextEngine SHALL 优先填充高相关性内容的 L0，在 Token 预算允许时逐步升级到 L1 和 L2
3. WHEN Token 预算不足以容纳所有 L2 内容时，THE ContextEngine SHALL 保留高优先级内容的 L2，并将低优先级内容降级为 L1 或 L0
4. THE ContextEngine SHALL 从多个来源组装上下文，包括记忆、Skill、用户画像和会话历史
5. FOR ALL 已启用 Skill，THE ContextEngine SHALL 在初次组装时注入其 `name + description`，并仅在判定需要时再加载该 Skill 的 `body`
6. THE ContextEngine SHALL 使用当前 query 与最近若干轮会话消息作为 Skill 命中判断输入，并基于名称精确命中、描述关键词匹配和语义相似度的混合评分决定是否加载 Skill `body`
7. THE ContextEngine SHALL 仅加载分数超过阈值的 Skill `body`，并按分数降序在 Token 预算约束下最多加载有限个 Skill 正文
8. WHEN 系统中没有任何启用 Skill 时，THE ContextEngine SHALL 跳过 Skill catalog 加载、命中判断和正文加载逻辑，且不得因此返回错误
9. WHEN 上下文组装完成时，THE ContextEngine SHALL 返回组装后的内容和实际消耗的 Token 估算值

### 需求 2：会话管理

**用户故事：** 作为 AI Agent 开发者，我希望中间件提供完整的会话管理能力，以便跟踪每个会话的状态和消息历史。

#### 验收标准

1. THE SessionManager SHALL 为每个会话维护独立的状态，包括消息历史、元数据和创建/更新时间戳
2. THE SessionManager SHALL 支持可配置的消息历史上限，默认保留最近 50 条消息
3. THE SessionManager SHALL 将消息存储在独立的 `session_messages` 表中（每条消息一行，追加写入），会话元数据存储在 `sessions` 表中，避免 JSONB 全量更新的性能问题
4. WHEN 请求访问不存在的会话时，THE SessionManager SHALL 自动创建新会话并返回
5. THE SessionManager SHALL 支持会话克隆操作，生成原会话的深拷贝用于异步操作
6. WHEN 会话数据发生变更时，THE SessionManager SHALL 将变更通过有界同步队列异步批量持久化到 PostgreSQL，队列满时产生背压阻塞写入方
7. THE SessionManager SHALL 为每个会话维护聚合元数据，包括 `commit_count`、`contexts_used`、`skills_used`、`tools_used`、`llm_token_usage` 和 `embedding_token_usage`
8. THE ContextEngine SHALL 支持在 ingest 或等价写入请求中接收可选的 `used_contexts`、`used_skills` 和 `tool_calls` 信息，并将其持久化为会话使用痕迹
9. WHEN 会话被提交、压缩或摄入新使用痕迹时，THE SessionManager SHALL 更新对应聚合元数据计数

### 需求 3：上下文压缩（Compact）

**用户故事：** 作为 AI Agent 开发者，我希望上下文引擎在对话过程中自动压缩历史上下文，以便在长对话中保持上下文连续性而不超出 Token 限制。

#### 验收标准

1. WHEN 满足以下任一触发条件时，THE CompactProcessor SHALL 异步执行上下文压缩：Token 预算使用率达到 `compact_budget_ratio`、新增 Token 累计达到 `compact_token_threshold`、对话轮次累计达到 `compact_turn_threshold`、距上次压缩间隔达到 `compact_interval_min` 且至少有 1 轮新内容
2. THE CompactProcessor SHALL 调用 LLM API 对历史消息生成摘要
3. THE CompactProcessor SHALL 从对话中提取关键记忆（事实、偏好、决策等）并持久化
4. THE CompactProcessor SHALL 从对话与历史归档数据中提取并合并用户画像，将用户长期关注主题、偏好、目标和稳定约束写入按 `tenant_id + user_id` 隔离的持久画像存储
5. THE CompactProcessor SHALL 通过 goroutine 异步执行压缩，压缩过程不阻塞当前请求返回
6. THE CompactProcessor SHALL 在压缩前无损保存原始上下文数据，压缩结果不替代原始消息本体
7. WHILE 同一会话已有一个压缩任务在执行时，THE CompactProcessor SHALL 拒绝重复触发，确保同一会话同时只有一个压缩任务运行
8. WHEN 进程收到 SIGTERM 或 SIGINT 信号时，THE CompactProcessor SHALL 刷新所有未提交缓冲区，确保数据不丢失

### 需求 4：CLI 管理员认证与管理操作

**用户故事：** 作为运维人员，我希望通过 CLI 登录并管理 ContextOS，以便安全地执行模型、Skill、API Key 和会话等管理操作。

#### 验收标准

1. THE CLI SHALL 基于 cobra 库实现，支持单次命令模式和交互式 REPL 模式
2. THE CLI SHALL 使用管理员账号密码进行认证，管理员认证仅用于 CLI 和管理 API
3. THE ContextEngine SHALL 支持在启动配置文件中声明默认管理员账号密码；当系统中尚无管理员账号时，服务启动时 SHALL 自动初始化该默认管理员
4. WHEN 用户直接输入 `ctx` 不带子命令时，THE CLI SHALL 进入交互式 REPL 模式，显示欢迎信息和提示符
5. THE CLI SHALL 在交互式模式中支持以下斜杠命令：`/admin`、`/provider`、`/model`、`/skill`、`/session`、`/memory`、`/search`、`/apikey`、`/migrate`、`/logs`、`/status`、`/help`、`/logout`、`/exit`
6. THE CLI SHALL 支持以下全局标志：`--config`、`--output`、`--tenant`、`--user`
7. THE CLI SHALL 从配置文件加载服务端地址、默认管理员账号密码和默认参数
8. WHEN 配置文件中存在管理员账号密码时，THE CLI SHALL 在 REPL 启动时自动尝试登录管理 API
9. WHEN 自动登录失败时，THE CLI SHALL 提示用户重新输入管理员账号密码，密码输入时不回显
10. THE CLI SHALL 支持通过 `/admin` 命令新增管理员账号和修改管理员密码
11. THE ContextEngine SHALL 仅在系统中不存在任何管理员账号时允许调用 `/api/v1/auth/setup`；当管理员已存在时再次调用 SHALL 返回冲突错误
12. THE ContextEngine SHALL 通过管理员管理接口支持 `list`、`create`、`update-password` 和 `disable` 操作；v1 不要求支持硬删除管理员
13. THE CLI SHALL 支持 `/logout` 命令清除本地保存的管理员登录态
14. WHEN 输出格式指定为 `table` 时，THE CLI SHALL 以人类可读表格展示结果；WHEN 输出格式指定为 `json` 时，THE CLI SHALL 输出结构化 JSON
15. THE CLI SHALL 在交互式模式中支持命令自动补全和历史记录

### 需求 5：数据隔离

**用户故事：** 作为平台运营者，我希望系统按租户、用户和会话三个维度隔离数据，以便不同租户和用户的上下文互不可见。

#### 验收标准

1. THE ContextEngine SHALL 使用 `tenant_id`、`user_id`、`session_id` 作为请求隔离维度
2. THE ContextEngine SHALL 将归档数据按 `tenant_id + user_id` 隔离存储，包括记忆、用户画像和压缩摘要
3. THE ContextEngine SHALL 将实时会话数据按 `tenant_id + user_id + session_id` 隔离存储，包括会话元数据、消息历史和会话缓存
4. WHEN 执行向量数据库查询时，THE VectorStore SHALL 注入 `tenant_id` 和 `user_id` 过滤字段，确保查询结果仅包含当前租户和用户的数据
5. WHEN 请求未携带 `tenant_id` 时，THE ContextEngine SHALL 使用默认值 `"default"`
6. WHEN 请求未携带 `user_id` 时，THE ContextEngine SHALL 使用默认值 `"default"`
7. THE SessionManager SHALL 以 `tenant_id + user_id + session_id` 三元组作为会话唯一标识
8. WHEN 访问会话数据时，THE SessionManager SHALL 强制校验请求中的 `tenant_id`、`user_id`、`session_id` 与会话归属一致，拒绝跨租户或跨用户访问
9. THE ContextEngine SHALL 确保同一 `user_id` 在不同 `tenant_id` 下是完全独立的身份，记忆、会话和上下文互不可见
10. THE ContextEngine SHALL 将 Skill 视为全局共享资源，Skill 不参与租户隔离

### 需求 6：向量存储抽象

**用户故事：** 作为 AI Agent 开发者，我希望向量存储支持多后端切换，以便根据部署规模选择合适的向量数据库。

#### 验收标准

1. THE VectorStore SHALL 定义统一的 Go 接口，包含 `Upsert`、`Search`、`Delete` 和 `Init` 方法
2. THE VectorStore SHALL 支持 pgvector、Elasticsearch 和 Milvus 三种后端实现
3. THE EmbeddingProvider SHALL 通过 `Dimension()` 方法声明向量维度，VectorStore 在初始化时根据该维度动态创建向量列和索引，迁移 SQL 不硬编码向量维度
4. WHEN 执行向量搜索时，THE VectorStore SHALL 接受租户过滤参数（`tenant_id`、`user_id`），确保搜索结果符合数据隔离要求
5. FOR ALL 有效的向量记录，执行 `Upsert` 后再执行 `Search`（使用相同向量和过滤条件），SHALL 返回包含该记录的结果集

### 需求 7：上下文检索

**用户故事：** 作为 AI Agent 开发者，我希望上下文引擎支持语义检索和模式检索，以便精准召回与当前对话相关的历史上下文。

#### 验收标准

1. WHEN 执行语义检索时，THE ContextEngine SHALL 将查询文本转换为嵌入向量，在向量数据库中执行带隔离过滤的相似度搜索，并按 L0 → L1 → L2 渐进加载结果内容
2. WHEN 执行模式检索时，THE ContextEngine SHALL 支持关键词搜索、正则匹配和文件通配符三种模式
3. THE PatternSearch SHALL 仅搜索当前请求作用域内的归档文本与会话文本，包括 `memory_facts`、`compact_checkpoints`、`user_profiles` 和当前会话消息；v1 不搜索文件系统，也不将 Skill 正文纳入 PatternSearch 范围
4. WHEN Token 预算有限时，THE ContextEngine SHALL 优先填充高相关性评分的 L0 摘要，在预算允许时升级到 L1 和 L2 详情
5. THE ContextEngine SHALL 对检索结果执行去重、评分排序和单条截断，确保注入上下文不超过配置的 Token 预算

### 需求 8：工具系统

**用户故事：** 作为 AI Agent 开发者，我希望中间件提供可扩展的工具系统，以便 Agent 能够动态注册和调用各类工具。

#### 验收标准

1. THE ToolRegistry SHALL 定义统一的 Tool 接口，包含 `Name()`、`Description()`、`Schema()` 和 `Execute()` 四个方法
2. THE ToolRegistry SHALL 支持运行时动态注册和注销工具
3. THE ToolRegistry SHALL 支持三种工具类型：Skill、Command、MCP
4. THE ContextEngine SHALL 支持 Skill 声明关联工具；Skill 文档中的工具定义至少包含 `name`、`description`、`input_schema` 和 `binding`
5. WHEN Skill 被启用时，THE ToolRegistry SHALL 注册该 Skill 声明的工具，使 Agent 可通过统一 Tool 接口调用它们
6. THE HookSystem SHALL 支持事件驱动的生命周期钩子，包括 `afterTurn`、`beforePrompt`、`compact` 和 `tool.post_call`
7. WHEN 工具执行完成后，THE HookSystem SHALL 触发 `tool.post_call` 钩子，传递工具名称、参数和执行结果

### 需求 9：分布式部署与集群

**用户故事：** 作为平台运维人员，我希望系统支持分布式部署，以便通过水平扩展应对高并发场景。

#### 验收标准

1. THE ContextEngine SHALL 采用无状态计算节点加共享存储层的架构，计算节点不持有会话状态
2. THE ContextEngine SHALL 基于 `session_id` 的一致性哈希实现会话亲和性，作为性能优化而非强依赖
3. WHEN 执行 Compact 操作时，THE CompactProcessor SHALL 通过 Redis 分布式锁确保同一会话的压缩操作在集群中互斥执行
4. THE ContextEngine SHALL 实现三层缓存架构：本地 LRU 缓存 → Redis 缓存 → PostgreSQL 持久存储
5. WHEN 进程收到 SIGTERM 信号时，THE ContextEngine SHALL 执行优雅关闭：停止接收新请求 → 刷新缓冲区 → 释放分布式锁 → 退出进程
6. THE ContextEngine SHALL 暴露 `/healthz` 和 `/readyz` 两个健康检查端点
7. WHEN 节点崩溃恢复启动时，THE ContextEngine SHALL 执行 Redis 与 PostgreSQL 之间的数据对账，修复不一致状态

### 需求 10：数据一致性与高可用

**用户故事：** 作为平台运维人员，我希望系统在写入性能和数据一致性之间取得平衡，并支持高可用部署。

#### 验收标准

1. THE ContextEngine SHALL 采用 WriteRedisFirst 写入策略：写操作先写入 Redis，再异步批量同步到 PostgreSQL
2. WHEN 执行 Compact 操作时，THE CompactProcessor SHALL 使用分布式锁加 PostgreSQL 事务确保压缩操作的原子性
3. THE ContextEngine SHALL 支持 PostgreSQL 通过 Patroni 实现同步复制高可用
4. THE ContextEngine SHALL 支持 Redis 通过 Sentinel 或 Cluster 模式实现高可用
5. IF Redis 写入成功但 PostgreSQL 异步同步失败，THEN THE ContextEngine SHALL 记录失败事件并在下次同步周期重试，确保最终一致性

### 需求 11：配置管理与序列化

**用户故事：** 作为 AI Agent 开发者，我希望系统提供灵活的配置管理，以便通过配置文件和环境变量控制系统行为。

#### 验收标准

1. THE ContextEngine SHALL 基于 viper 加载配置，支持 JSON、YAML 和 TOML 格式
2. THE ContextEngine SHALL 支持通过环境变量覆盖配置文件中的参数，环境变量前缀为 `CONTEXTOS_`
3. THE ContextEngine SHALL 定义配置结构体并提供默认值，包括服务端口、Redis 地址和连接模式（standalone/sentinel/cluster，默认 standalone）、PostgreSQL 连接串、LLM API 端点、Embedding API 端点、Token 预算和 Compact 触发阈值
4. THE ContextEngine SHALL 支持在配置中声明默认管理员账号密码
5. THE ConfigParser SHALL 将配置文件解析为强类型的 Go 结构体
6. THE ConfigPrinter SHALL 将 Go 配置结构体格式化输出为有效配置内容
7. FOR ALL 有效的配置结构体，解析配置文件后再格式化输出再解析，SHALL 产生等价的配置结构体

### 需求 12：运行日志与可观测性

**用户故事：** 作为平台运维人员，我希望系统提供分层的运行日志和完善的可观测性支持，以便监控系统运行状态、排查问题和满足合规要求。

#### 验收标准

1. THE ContextEngine SHALL 实现四层日志分类：系统日志、请求日志、引擎日志、审计日志
2. THE ContextEngine SHALL 使用结构化日志（JSON 格式），每条日志包含 `ts`、`level`、`msg`、`trace_id`、`session_id`、`tenant`、`component` 和 `duration_ms` 字段
3. THE ContextEngine SHALL 支持 stdout/stderr、文件轮转和远程推送三种日志输出目标
4. THE ContextEngine SHALL 支持按组件和级别独立配置日志级别
5. THE ContextEngine SHALL 将审计日志与普通运行日志分离存储，审计日志写入独立 PostgreSQL 表，采用 append-only 模式
6. THE ContextEngine SHALL 在审计日志中记录管理员操作、API Key 变更、会话删除、记忆删除等敏感操作
7. THE ContextEngine SHALL 集成 OpenTelemetry，为关键操作生成分布式追踪 Span
8. THE ContextEngine SHALL 通过 Prometheus 客户端暴露 `/metrics` 指标端点
9. WHEN 上下文组装耗时超过慢查询阈值时，THE ContextEngine SHALL 记录包含组装详情的警告日志
10. THE ContextEngine SHALL 记录每次 LLM 调用和 Embedding 调用的 Token 消耗审计
11. THE ContextEngine SHALL 将 Token 审计记录写入独立 PostgreSQL 表，支持按租户、时间范围和调用类型聚合查询
12. THE ContextEngine SHALL 提供 Token 用量查询 API（`GET /api/v1/usage/tokens`）
13. THE CLI SHALL 支持查询和过滤审计日志及 Token 用量
14. THE ContextEngine SHALL 支持按请求显式启用 Operation Telemetry，并在响应中返回结构化 `telemetry.id` 和 `telemetry.summary`
15. THE Operation Telemetry summary SHALL 至少支持以下分组中的适用子集：`operation`、`status`、`duration_ms`、`tokens`、`vector`、`skill`、`compact`、`profile`、`errors`
16. THE ContextEngine SHALL 在 HTTP 响应头中包含服务端处理耗时 `X-Process-Time`

### 需求 13：HTTP API 服务

**用户故事：** 作为 AI Agent 开发者，我希望通过 HTTP API 与上下文引擎交互，以便将其集成到现有的 Agent 应用中。

#### 验收标准

1. THE ContextEngine SHALL 提供 RESTful HTTP API，基于 gin、echo 或 chi 框架实现
2. THE ContextEngine SHALL 在每个 HTTP 请求中从 `X-API-Key` 头提取服务认证 Key，验证通过后从 `X-Tenant-ID` 和 `X-User-ID` 头提取租户和用户标识
3. WHEN 请求未携带有效 `X-API-Key` 且系统处于生产模式时，THE ContextEngine SHALL 返回 HTTP 401 Unauthorized
4. THE ContextEngine SHALL 提供以下业务 API 端点：
   - `POST /api/v1/context/assemble`
   - `POST /api/v1/context/ingest`
   - `CRUD /api/v1/sessions`
   - `POST /api/v1/memory/search`
   - `POST /api/v1/memory/store`
   - `DELETE /api/v1/memory/:id`
   - `POST /api/v1/tools/execute`
   - `GET /api/v1/tasks/:task_id`
   - `POST /api/v1/uploads/temp`
5. THE ContextEngine SHALL 为业务 API 使用 JSON 请求体，并至少满足以下最小字段约束：
   - `POST /api/v1/context/assemble` 请求体至少包含 `session_id`、`query`，可选 `token_budget`
   - `POST /api/v1/context/ingest` 请求体至少包含 `session_id`、`messages[]`
   - `POST /api/v1/memory/search` 请求体至少包含 `query`，可选 `limit`
   - `POST /api/v1/memory/store` 请求体至少包含 `content`，可选 `category` 和 `metadata`
   - `POST /api/v1/tools/execute` 请求体至少包含 `session_id`、`tool_name` 和 `params`
6. THE ContextEngine SHALL 提供以下管理 API 端点：
   - `POST /api/v1/auth/setup`
   - `POST /api/v1/auth/login`
   - `POST /api/v1/auth/verify`
   - `CRUD /api/v1/admin/users`
   - `CRUD /api/v1/admin/apikeys`
   - `CRUD /api/v1/admin/models`
   - `CRUD /api/v1/admin/providers`
   - `CRUD /api/v1/skills`
   - `CRUD /api/v1/webhooks`
   - `GET /api/v1/observer/system`
   - `GET /api/v1/observer/queue`
7. THE ContextEngine SHALL 对除 `/api/v1/auth/*` 之外的管理 API 使用 `Authorization: Bearer <admin_session_token>` 进行管理员认证
8. THE ContextEngine SHALL 在 `/api/v1/auth/verify` 返回当前管理员会话的 `user_id`、`username` 和 `expires_at`
9. THE ContextEngine SHALL 在 API 响应中包含请求追踪 ID（`X-Request-Id` 头）
10. THE ContextEngine SHALL 支持对 `assemble`、`ingest`、`memory/search`、`tools/execute` 和 `skills` 导入请求通过 JSON 字段 `telemetry` 显式开启请求级 telemetry

### 需求 14：Agent 接入层

**用户故事：** 作为 AI Agent 开发者，我希望 ContextOS 提供多种接入方式，以便不同语言和框架的 Agent 应用都能获得一致的上下文管理能力。

#### 验收标准

1. THE ContextEngine SHALL 提供 HTTP API 接入方式；认证通过 `X-API-Key` 头传递，租户和用户通过 `X-Tenant-ID`、`X-User-ID` 头传递，`session_id` 通过请求体传递
2. THE ContextEngine SHALL 提供 Go SDK 接入方式；SDK 初始化时传入 `api_key`，每次调用通过参数传入 `tenant_id`、`user_id` 和 `session_id`
3. THE ContextEngine SHALL 作为 MCP Server 运行；`api_key` 通过启动参数传入，每个 MCP 工具的 `inputSchema` 中包含 `tenant_id`、`user_id`、`session_id`
4. THE ContextEngine SHALL 支持 Webhook 事件回调机制；Webhook 订阅通过管理 API 注册，事件发生时 ContextOS 主动 POST 通知到回调 URL
5. THE ContextEngine SHALL 在 Webhook POST 请求中发送统一事件载荷，至少包含 `id`、`type`、`tenant_id`、`user_id`、`session_id`、`occurred_at` 和 `payload`
6. FOR ALL 请求式接入方式（HTTP API、Go SDK、MCP），THE ContextEngine SHALL 使用相同的核心引擎逻辑，确保同一请求产生一致结果

### 需求 15：接口驱动设计

**用户故事：** 作为 AI Agent 开发者，我希望所有外部依赖都通过 Go 接口抽象，以便于测试和替换具体实现。

#### 验收标准

1. THE ContextEngine SHALL 为以下外部依赖定义 Go 接口：`LLMClient`、`EmbeddingProvider`、`VectorStore`、`CacheStore`、`SessionStore`、`ProfileStore`
2. THE ContextEngine SHALL 通过依赖注入将接口实现传入核心模块，核心模块不直接依赖具体实现
3. FOR ALL 定义的接口，THE ContextEngine SHALL 提供至少一个生产实现和一个内存模拟实现

### 需求 16：数据库自动迁移

**用户故事：** 作为运维人员，我希望系统在启动时自动检测并执行数据库 Schema 迁移，以便连接外部 PostgreSQL 时无需手动建库建表。

#### 验收标准

1. THE ContextEngine SHALL 在启动时自动检测 PostgreSQL 中是否存在所需表结构，若不存在则自动执行建表和索引创建
2. THE ContextEngine SHALL 使用版本化的增量迁移机制，在 `schema_migrations` 表中记录已执行迁移版本号，仅执行未执行过的迁移
3. THE ContextEngine SHALL 在启动时自动检测 pgvector 扩展是否已安装，若未安装且当前数据库用户有权限则自动执行 `CREATE EXTENSION IF NOT EXISTS vector`
4. THE CLI SHALL 提供 `ctx migrate` 子命令，支持 `up`、`down`、`status`
5. WHEN 多个节点同时启动时，THE ContextEngine SHALL 通过 PostgreSQL advisory lock 确保迁移操作在集群中互斥执行
6. THE ContextEngine SHALL 支持通过 `auto_migrate` 配置控制是否在启动时自动执行迁移
7. THE ContextEngine SHALL 在连接外部 PostgreSQL 时仅要求目标数据库已存在，不自动创建数据库

### 需求 17：模型与供应商动态管理

**用户故事：** 作为运维人员，我希望通过 CLI 和 API 动态添加、启用和停用 LLM 模型、Embedding 模型及模型供应商，以便在不重启服务的情况下切换模型配置。

#### 验收标准

1. THE CLI SHALL 提供 `/provider` 交互命令，支持添加、列出、删除和更新供应商
2. THE CLI SHALL 提供 `/model` 交互命令，支持添加、列出、启用、停用和设置默认模型
3. THE ContextEngine SHALL 统一使用 OpenAI 兼容协议调用所有模型供应商
4. THE ContextEngine SHALL 将模型和供应商配置持久化到 PostgreSQL，支持运行时动态加载，无需重启服务
5. THE ContextEngine SHALL 提供对应的管理 API 端点（`/api/v1/admin/models` 和 `/api/v1/admin/providers`）
6. WHEN 默认模型被停用时，THE ContextEngine SHALL 拒绝停用操作并返回错误
7. WHEN 将 embedding 模型设为默认时，若新模型维度与当前默认 embedding 模型不同且向量表中已有数据，THE ContextEngine SHALL 拒绝操作并返回错误
8. THE ContextEngine SHALL 在模型配置变更时通过 Redis Pub/Sub 通知集群节点刷新本地模型缓存

### 需求 18：Skill 动态管理

**用户故事：** 作为 AI Agent 开发者，我希望通过 CLI 和 API 动态添加、启用和停用 Skill，以便灵活管理 Agent 的能力集。

#### 验收标准

1. THE CLI SHALL 提供 `/skill` 交互命令，支持 `add`、`list`、`enable`、`disable`、`remove`、`info`
2. THE CLI SHALL 以统一 Skill 文档格式导入 Skill；该格式至少包含 `name`、`description` 和 `body`，并可选声明 `tools[]`
3. THE ContextEngine SHALL 将 Skill 元数据、正文和工具声明持久化到 PostgreSQL 的 `skills` 表中，Skill 作为全局共享资源存储
4. THE ContextEngine SHALL 在上下文组装时默认注入启用 Skill 的 `name + description`，并在需要时再加载对应 `body`
5. THE ContextEngine SHALL 将当前 query 与最近 4 轮会话消息作为 Skill 命中判断输入
6. THE ContextEngine SHALL 对每个候选 Skill 计算命中分数；分数至少包含名称精确命中、描述关键词匹配和语义相似度三个因子
7. WHEN 某个 Skill 的命中分数超过阈值时，THE ContextEngine SHALL 允许加载该 Skill 的 `body`
8. THE ContextEngine SHALL 按分数降序并受 Token 预算限制加载 Skill `body`，同一轮上下文组装最多加载有限个 Skill 正文
9. WHEN 系统中没有任何启用 Skill 时，THE ContextEngine SHALL 跳过 Skill 相关加载和匹配逻辑，正常返回不含 Skill 内容的上下文结果
10. WHEN Skill 被停用时，THE ToolRegistry SHALL 自动注销该 Skill 关联的所有工具，停用 Skill 不参与上下文组装
11. WHEN Skill 被启用时，THE ToolRegistry SHALL 自动注册该 Skill 关联的工具，启用 Skill 恢复参与上下文组装
12. THE ContextEngine SHALL 提供对应的管理 API 端点（`CRUD /api/v1/skills`）
13. THE ContextEngine SHALL 在 Skill 导入或更新时为 `name + description` 预计算并持久化语义匹配向量，用于运行时 `semantic_hit`
14. WHEN Skill 状态变更时，THE ContextEngine SHALL 通过 Redis Pub/Sub 通知集群节点刷新本地 Skill 缓存
15. THE ContextEngine SHALL 在 HTTP API 中支持两种 Skill 导入方式：直接提交结构化 `SkillDocument`，或先上传临时文件再通过 `temp_file_id` 导入
16. THE HTTP API SHALL 不接受指向服务端宿主机本地路径的 Skill 导入参数；本地文件或目录上传由 CLI/SDK 在客户端侧完成
17. WHEN Skill 导入请求显式指定 `wait=false` 时，THE ContextEngine MAY 以异步任务方式处理导入，并返回 `accepted` 状态和 `task_id`

### 需求 19：服务接入认证

**用户故事：** 作为平台运营者，我希望通过简单的 API Key 机制对接入 ContextOS 的上游服务进行认证，以便控制哪些服务可以调用中间件 API。

#### 验收标准

1. THE ContextEngine SHALL 通过 API Key 对接入服务进行认证
2. THE CLI SHALL 提供 `/apikey` 交互命令，支持 `create`、`list`、`revoke`
3. WHEN 通过 `/apikey create` 颁发 API Key 时，THE CLI SHALL 显示生成的完整 Key（仅显示一次），并提示用户妥善保存
4. THE ContextEngine SHALL 将 API Key 持久化到 PostgreSQL 的 `api_keys` 表，存储 Key 的 SHA-256 哈希值、名称、状态和创建时间
5. WHEN 接入服务通过 API Key 认证后，THE ContextEngine SHALL 从 HTTP 头或非 HTTP 请求参数中提取 `tenant_id`、`user_id`、`session_id`，构造请求上下文；API Key 不绑定特定租户
6. WHEN 请求未携带有效 API Key 且系统未处于开发模式时，THE ContextEngine SHALL 返回认证失败响应
7. WHEN 未颁发任何 API Key 且系统显式配置为开发模式时，THE ContextEngine SHALL 跳过服务认证
8. WHEN API Key 被吊销时，THE ContextEngine SHALL 立即使该 Key 失效，并通过 Redis Pub/Sub 通知集群所有节点

### 需求 20：任务追踪与诊断

**用户故事：** 作为平台运维人员和 Agent 开发者，我希望系统为长耗时任务和单次请求提供统一的任务追踪与诊断能力，以便快速定位问题并验证系统状态。

#### 验收标准

1. THE ContextEngine SHALL 提供统一的 TaskTracker，对后台任务维护 `pending`、`running`、`completed` 和 `failed` 四种状态
2. WHEN 后台 Compact、异步 Skill 导入或其他长耗时流程被异步触发时，THE ContextEngine SHALL 返回 `task_id`
3. THE ContextEngine SHALL 提供任务查询端点 `GET /api/v1/tasks/:task_id`，返回任务状态、任务类型、开始/结束时间、结果摘要和错误信息
4. THE ContextEngine SHALL 提供观察接口 `GET /api/v1/observer/system` 和 `GET /api/v1/observer/queue`，用于查看系统健康、队列积压和关键组件状态
5. THE ContextEngine SHALL 对 `/healthz` 保持免认证可访问，以便负载均衡器和监控系统进行存活检查
6. THE CLI SHALL 提供 `ctx doctor` 或等价诊断命令，检查配置、PostgreSQL、Redis、模型配置、磁盘空间和关键依赖可用性
7. WHEN 请求显式启用 telemetry 时，THE ContextEngine SHALL 返回紧凑的阶段性执行摘要，而不是依赖客户端解析原始日志
