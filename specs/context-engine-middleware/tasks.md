# 实施计划：ContextOS — Go 上下文引擎中间件

## 概述

本计划保留原有的增量式推进策略：先搭建项目骨架和核心接口，再逐步实现存储、认证、会话、检索、上下文组装、管理能力和接入层。所有属性测试使用 `pgregory.net/rapid` 库，属性编号与 `design.md` 保持一致。

## 任务

- [x] 1. 项目初始化与核心类型定义
  - [x] 1.1 初始化 Go module，创建目录结构和 go.mod
    - 创建 `go.mod`（module 名 `github.com/contextos/contextos`）
    - 创建目录结构：`cmd/ctx/`、`internal/engine/`、`internal/store/`、`internal/auth/`、`internal/config/`、`internal/migrate/`、`internal/log/`、`internal/cluster/`、`internal/api/`、`internal/sdk/`、`internal/mcp/`、`internal/webhook/`、`internal/cli/`、`internal/mock/`
    - 添加核心依赖：`github.com/gin-gonic/gin`、`github.com/jackc/pgx/v5`、`github.com/redis/go-redis/v9`、`github.com/spf13/cobra`、`github.com/spf13/viper`、`pgregory.net/rapid`
    - _需求: 15.1, 15.2_

  - [x] 1.2 定义核心接口和数据结构
    - 创建 `internal/types/types.go`：定义 `Message`、`ToolCall`、`UsageRecord`、`MemoryFact`、`UserProfile`、`SessionMeta`、`ContentLevel`、`ContentBlock`、`RequestContext`
    - 创建 `internal/types/interfaces.go`：定义 `VectorStore`、`EmbeddingProvider`、`LLMClient`、`CacheStore`、`SessionStore`、`ProfileStore`、`TaskTracker`、`Tool`、`Hook`
    - 创建 `internal/types/engine.go`：定义 `Engine`、`AssembleRequest/Response`、`IngestRequest/Response`、`MemorySearchRequest`、`MemoryStoreRequest`、`ToolExecuteRequest`、`AuthVerifyResponse`、`TaskRecord`、`TaskGetResponse`、`TelemetryOption`、`OperationTelemetry`、`WebhookEvent`
    - 创建 `internal/types/auth.go`：定义 `AdminUser`、`AdminSession`、`APIKeyRecord`
    - 创建 `internal/types/skill.go`：定义 `SkillDocument`、`SkillMeta`、`SkillToolBinding`
    - 创建 `internal/types/errors.go`：定义 `AppError` 和错误码常量
    - _需求: 15.1, 1.1, 2.1, 6.1, 8.1, 18.2_

- [x] 2. 配置管理与日志系统
  - [x] 2.1 实现配置加载模块
    - 创建 `internal/config/config.go`：定义 `Config`、`ServerConfig`、`AdminConfig`、`RedisConfig`（含 `mode` 字段支持 standalone/sentinel/cluster）、`PostgresConfig`、`EngineConfig`、`LogConfig`、`MigrateConfig`
    - 基于 viper 实现配置加载：支持 YAML/JSON/TOML、`CONTEXTOS_` 环境变量覆盖、默认值
    - `RedisConfig.Mode` 默认值为 `standalone`；根据 mode 初始化对应的 `go-redis` 客户端（`redis.NewClient` / `redis.NewFailoverClient` / `redis.NewClusterClient`）
    - 支持默认管理员配置：`admin.bootstrap_username`、`admin.bootstrap_password`
    - 支持 CLI 登录配置：`admin.username`、`admin.password`
    - 实现 `ConfigParser`（文件 -> 结构体）和 `ConfigPrinter`（结构体 -> 文件内容）
    - _需求: 11.1, 11.2, 11.3, 11.4, 11.5, 11.6, 11.7_

  - [x]* 2.2 编写配置序列化往返属性测试
    - **Property 13: 配置序列化往返一致性**
    - **验证: 需求 11.5, 11.6, 11.7**

  - [x]* 2.3 编写环境变量覆盖属性测试
    - **Property 14: 环境变量覆盖配置**
    - **验证: 需求 11.2**

  - [x] 2.4 实现结构化日志系统
    - 创建 `internal/log/logger.go`：基于 `go.uber.org/zap` 实现结构化 JSON 日志
    - 支持四层日志分类（系统/请求/引擎/审计）、按组件独立配置级别
    - 每条日志包含 `ts`、`level`、`msg`、`trace_id`、`session_id`、`tenant`、`component`、`duration_ms`
    - 支持 stdout/stderr 和文件轮转输出
    - _需求: 12.1, 12.2, 12.3, 12.4_

  - [x]* 2.5 编写结构化日志格式属性测试
    - **Property 28: 结构化日志格式**
    - **验证: 需求 12.2**

  - [x]* 2.6 编写日志级别过滤属性测试
    - **Property 29: 日志级别过滤**
    - **验证: 需求 12.4**

- [x] 3. 检查点
  - 确保当前测试通过，再进入存储和迁移阶段。

- [x] 4. 数据库迁移与存储层
  - [x] 4.1 实现数据库迁移管理器
    - 创建 `internal/migrate/migrator.go`：实现 `Run`、`Status`、`Rollback`
    - 使用 Go embed 内嵌 SQL 迁移文件，通过 PostgreSQL advisory lock 防止并发迁移
    - 创建迁移 SQL 文件：
      - `001_init_schema`：`sessions`、`session_messages`、`session_usage_records`、`compact_checkpoints`、`memory_facts`、`user_profiles`
      - `002_auth_tables`：`api_keys`、`admin_users`
      - `003_audit_tables`：`audit_logs`、`token_usage`
      - `004_model_tables`：`model_providers`、`models`、`skills`（含 `tool_bindings`、`catalog_embedding`）、`webhook_subscriptions`、`tasks`
      - `005_vector_extension`：`pgvector` 扩展和 `vector_items` 基础表结构（不含向量列，向量列由 `VectorStore.Init()` 动态创建）
    - 创建 `schema_migrations` 版本追踪表
    - _需求: 16.1, 16.2, 16.3, 16.4, 16.5, 16.6, 16.7_

  - [ ]* 4.2 编写迁移幂等性属性测试
    - **Property 38: 迁移幂等性**
    - **验证: 需求 16.2**

  - [ ]* 4.3 编写迁移并发互斥属性测试
    - **Property 39: 迁移并发互斥**
    - **验证: 需求 16.5**

  - [x] 4.4 实现 SessionStore（PostgreSQL）
    - 创建 `internal/store/session_store.go`
    - 实现 `Load`、`Save`、`Delete`、`List`
    - `sessions` 表使用 `(tenant_id, user_id, id)` 复合主键
    - `session_messages` 表使用 `(tenant_id, user_id, session_id, seq)` 复合主键
    - 加载消息时按 `seq DESC` 取最近 `max_messages` 条
    - _需求: 2.1, 2.3, 5.3, 5.7, 5.8_

  - [x] 4.4.1 实现 ProfileStore（PostgreSQL）
    - 创建 `internal/store/profile_store.go`
    - 实现 `Load`、`Upsert`、`Search`
    - `user_profiles` 表按 `(tenant_id, user_id)` 主键 upsert
    - `Search` 仅搜索当前租户用户的画像摘要文本
    - 无画像记录时返回 `nil, nil`
    - _需求: 1.4, 3.4, 5.2, 7.3_

  - [ ]* 4.5 编写会话持久化往返属性测试
    - **Property 4: 会话持久化往返一致性**
    - **验证: 需求 2.3, 2.6**

  - [x] 4.6 实现 CacheStore（Redis）
    - 创建 `internal/store/cache_store.go`
    - 根据 `RedisConfig.Mode` 初始化对应客户端：standalone 使用 `redis.NewClient`、sentinel 使用 `redis.NewFailoverClient`、cluster 使用 `redis.NewClusterClient`
    - 实现 `Get`、`Set`、`Delete`、`SetNX`
    - _需求: 9.4, 10.1_

  - [x] 4.7 实现内存模拟存储
    - 创建 `internal/mock/` 下的模拟实现：`MemorySessionStore`、`MemoryCacheStore`、`MemoryVectorStore`、`MockLLMClient`、`MockEmbeddingProvider`
    - _需求: 15.3_

- [x] 5. 认证与隔离
  - [x] 5.1 实现服务 API Key 管理器
    - 创建 `internal/auth/apikey.go`
    - 实现 `Create`、`Verify`、`Revoke`、`List`
    - Key 格式：`ctx_` 前缀 + 随机字符串，存储 SHA-256 哈希
    - Redis Pub/Sub 通知集群节点刷新 API Key 缓存
    - _需求: 19.1, 19.2, 19.3, 19.4, 19.8_

  - [ ]* 5.2 编写 API Key 校验属性测试
    - **Property 17: API Key 校验正确性**
    - **验证: 需求 19.1, 19.4**

  - [ ]* 5.3 编写无效 API Key 拒绝属性测试
    - **Property 18: 无效 API Key 拒绝**
    - **验证: 需求 13.3, 19.6**

  - [x] 5.4 实现管理员认证
    - 创建 `internal/auth/admin.go`
    - 实现 `BootstrapDefaultAdmin`、`CreateAdmin`、`Login`、`VerifySession`、`UpdatePassword`、`HasAdmin`
    - `BootstrapDefaultAdmin` 仅在系统中无管理员时执行
    - 禁止禁用最后一个可用管理员
    - 密码使用 bcrypt 哈希存储
    - 登录成功后生成随机 `admin_session_token`，写入 Redis，默认 TTL 24h
    - _需求: 4.2, 4.3, 4.8, 4.9, 4.10, 4.11, 4.12_

  - [ ]* 5.5 编写默认管理员初始化幂等属性测试
    - **Property 19: 默认管理员初始化幂等**
    - **验证: 需求 4.3**

  - [ ]* 5.6 编写管理员登录属性测试
    - **Property 20: 管理员登录正确性**
    - **验证: 需求 4.2, 4.8, 4.9**

  - [x] 5.7 实现服务认证中间件与请求上下文提取
    - 创建 `internal/api/middleware.go`
    - 从 `X-API-Key` 提取并验证服务身份
    - 从 `X-Tenant-ID` / `X-User-ID` 提取租户和用户标识，默认 `"default"`
    - 将 `RequestContext` 注入 `gin.Context`
    - 开发模式下可跳过服务认证
    - _需求: 5.1, 5.5, 5.6, 13.2, 13.3, 19.5, 19.6, 19.7_

  - [x] 5.8 实现管理 API 认证中间件
    - 在 `internal/api/middleware.go` 中增加 `AdminAuthMiddleware`
    - 从 `Authorization: Bearer <admin_session_token>` 提取管理员登录态
    - 调用 `AdminAuth.VerifySession` 校验管理态
    - 对 `/api/v1/auth/*` 之外的管理端点启用该中间件
    - _需求: 4.2, 13.6_

- [x] 6. 检查点
  - 确保认证、配置和迁移相关测试通过，再进入会话和检索阶段。

- [x] 7. 会话管理器与三层缓存
  - [x] 7.1 实现 SessionManager
    - 创建 `internal/engine/session_manager.go`
    - 三层缓存查找：本地 LRU（带 TTL）-> Redis -> PostgreSQL -> 新建
    - 缓存键格式：`{tenant_id}:{user_id}:{session_id}`
    - 实现 `GetOrCreate`、`AddMessage`（WriteRedisFirst + SyncQueue 异步批量同步 PG）、`RecordUsage`、`Clone`
    - 实现 `SyncQueue`：有界 channel、双触发刷新、失败重试 3 次、死信队列
    - 维护会话聚合元数据：`commit_count`、`contexts_used`、`skills_used`、`tools_used`、`llm_token_usage`、`embedding_token_usage`
    - 将 `used_contexts`、`used_skills`、`tool_calls` 持久化到 `session_usage_records`
    - _需求: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 5.3, 5.7, 5.8, 9.4, 10.1_

  - [ ]* 7.2 编写会话消息历史上限属性测试
    - **Property 5: 会话消息历史上限**
    - **验证: 需求 2.2**

  - [ ]* 7.3 编写会话自动创建属性测试
    - **Property 6: 会话自动创建**
    - **验证: 需求 2.4**

  - [ ]* 7.4 编写会话克隆深拷贝属性测试
    - **Property 7: 会话克隆深拷贝**
    - **验证: 需求 2.5**

  - [ ]* 7.5 编写会话三元组隔离属性测试
    - **Property 8: 会话三元组隔离**
    - **验证: 需求 5.3, 5.7, 5.8**

  - [ ]* 7.6 编写会话使用痕迹聚合属性测试
    - **Property 8.5: 会话使用痕迹聚合**
    - **验证: 需求 2.7, 2.8, 2.9**
    - 覆盖场景：`used_contexts`、`used_skills`、`tool_calls` 写入后，`SessionMeta` 计数与 `session_usage_records` 一致

- [x] 8. 向量存储与检索引擎
  - [x] 8.1 实现 VectorStore 的 pgvector 后端
    - 创建 `internal/store/pgvector_store.go`
    - `Init` 根据 `EmbeddingProvider.Dimension()` 动态执行 `ALTER TABLE ... ADD COLUMN IF NOT EXISTS embedding vector(N)` 和创建 ivfflat 索引
    - 同时为 `skills.catalog_embedding` 列执行相同的动态 DDL
    - `Search` 注入 `tenant_id + user_id` 过滤条件
    - `Upsert` 支持批量插入/更新
    - _需求: 6.1, 6.2, 6.3, 6.4_

  - [ ]* 8.2 编写向量存储往返一致性属性测试
    - **Property 15: 向量存储往返一致性**
    - **验证: 需求 6.5**

  - [ ]* 8.3 编写租户用户过滤隔离属性测试
    - **Property 16: 租户用户过滤隔离**
    - **验证: 需求 5.2, 5.4, 6.4, 7.1**

  - [x] 8.4 实现 EmbeddingProvider
    - 创建 `internal/store/embedding_provider.go`
    - 调用 OpenAI 兼容 `/v1/embeddings`
    - `Dimension()` 返回配置的向量维度
    - _需求: 6.3, 17.3_

  - [x] 8.5 实现 RetrievalEngine
    - 创建 `internal/engine/retrieval.go`
    - `SemanticSearch`：查询文本 -> 嵌入向量 -> 带租户过滤的相似度搜索 -> 返回 L0 内容 -> 根据预算升级到 L1/L2
    - `PatternSearch`：签名包含 `RequestContext`，支持 keyword、grep、glob
    - PatternSearch v1 搜索范围限定为 `memory_facts`、`compact_checkpoints.summary`、`user_profiles.summary` 和当前会话消息
    - 不搜索文件系统，也不将 Skill 正文纳入 PatternSearch
    - `BatchLoadContent`：批量加载指定 ID 的 L1/L2 内容
    - 结果后处理：去重、评分排序、单条截断
    - _需求: 7.1, 7.2, 7.3, 7.4_

  - [ ]* 8.6 编写模式检索匹配正确性属性测试
    - **Property 37: 模式检索匹配正确性**
    - **验证: 需求 7.2**

- [x] 9. 检查点
  - 确保存储、会话、向量检索相关测试通过，再进入核心引擎阶段。

- [x] 10. 核心引擎：上下文组装与压缩
  - [x] 10.1 实现 LLMClient
    - 创建 `internal/store/llm_client.go`
    - 调用 OpenAI 兼容 `/v1/chat/completions`
    - 解析 token 用量信息
    - 支持重试（3 次指数退避）
    - _需求: 17.3_

  - [x] 10.2 实现 ContextBuilder
    - 创建 `internal/engine/context_builder.go`
    - 保持原有“两阶段：并行获取 + 串行编排”结构
    - 阶段 1 并行获取：user profile、memories、session history、Skill catalog
    - 阶段 2 串行编排：Token 预算分配、Skill 摘要注入、计算每个 Skill 的 `matchScore`、命中 Skill 的 body 按需加载、L1/L2 批量升级、超预算裁剪
    - 用户画像来自 `ProfileStore.Load(tenant_id, user_id)`，无画像时直接跳过
    - 当 `LoadCatalog` 返回空集合时，直接跳过 Skill 匹配和 `LoadBody` 逻辑，不得返回错误，也不得影响其他上下文来源组装
    - Skill 命中判断输入为当前 query + 最近 4 轮会话消息
    - `matchScore` 由名称精确命中、描述关键词匹配、语义相似度三部分组成
    - `keyword_hit` 使用 description 分词后的小写归一化词项匹配
    - `semantic_hit` 使用 Skill 预计算的 `catalog_embedding`
    - 当 `matchScore >= skill_body_load_threshold` 时允许加载正文，默认阈值 `0.9`
    - 同一轮最多加载 `max_loaded_skill_bodies` 个 Skill 正文，默认值 `2`
    - 若 query 明确点名某个 Skill，则该 Skill 直接视为强命中并优先加载
    - 若正文加载后超出 Token 预算，则回退该 Skill 正文，仅保留 `name + description`
    - 返回 `AssembleResponse`
    - _需求: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.8, 5.2, 5.3, 18.4, 18.5, 18.6, 18.9, 18.13_

  - [ ]* 10.3 编写渐进式组装优先级不变量属性测试
    - **Property 1: 渐进式组装优先级不变量**
    - **验证: 需求 1.2, 1.3, 7.3**

  - [ ]* 10.4 编写组装结果 Token 估算一致性属性测试
    - **Property 2: 组装结果 Token 估算一致性**
    - **验证: 需求 1.6**

  - [ ]* 10.5 编写内容层级大小有序性属性测试
    - **Property 3: 内容层级大小有序性**
    - **验证: 需求 1.1**

  - [ ]* 10.6 编写 Skill 渐进式加载属性测试
    - **Property 24: Skill 摘要先注入、正文按需加载**
    - **验证: 需求 1.5, 18.4**
    - 覆盖场景：无命中不加载正文、名称精确命中加载对应正文、多个 Skill 同时命中时按分数和 Top N 约束选择、预算不足时回退正文

  - [ ]* 10.6.1 编写无 Skill 路径短路属性测试
    - **Property 24.5: 无 Skill 时跳过 Skill 路径**
    - **验证: 需求 1.8, 18.9**
    - 覆盖场景：`LoadCatalog` 返回空集合时不调用 `LoadBody`、组装流程正常返回、其他上下文来源照常参与组装

  - [x] 10.7 实现 CompactProcessor
    - 创建 `internal/engine/compact_processor.go`
    - `EvaluateAndTrigger`：评估四种触发条件
    - `executeCompact`：获取 Redis 分布式锁 -> Clone session -> LLM 生成摘要 -> 提取记忆并向量化 -> 合并用户画像 -> 持久化 `CompactCheckpoint` -> 释放锁 -> 触发 Hook/Webhook
    - 用户画像合并来源：最近会话消息、既有画像摘要、长期记忆
    - 用户画像按 `(tenant_id, user_id)` upsert，失败仅记录错误日志，不回滚已完成的摘要和记忆写入
    - 并发控制：全局信号量 + 本地互斥 + Redis 分布式锁
    - _需求: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 9.3, 10.2_

  - [ ]* 10.8 编写 Compact 触发条件属性测试
    - **Property 9: Compact 触发条件正确性**
    - **验证: 需求 3.1**

  - [ ]* 10.9 编写 Compact 互斥执行属性测试
    - **Property 10: Compact 互斥执行**
    - **验证: 需求 3.7, 9.3, 10.2**

  - [ ]* 10.10 编写 Compact 无损保存属性测试
    - **Property 11: Compact 无损保存**
    - **验证: 需求 3.6**

  - [ ]* 10.11 编写 Compact 非阻塞属性测试
    - **Property 12: Compact 非阻塞**
    - **验证: 需求 3.5**

  - [ ]* 10.11.1 编写用户画像合并持久化属性测试
    - **Property 12.5: 用户画像合并持久化**
    - **验证: 需求 3.4**
    - 覆盖场景：同一租户用户多次画像更新保持单记录 upsert、稳定偏好保留、短期噪声不应永久写入

- [x] 11. 工具系统与钩子系统
  - [x] 11.1 实现 ToolRegistry
    - 创建 `internal/engine/tool_registry.go`
    - 支持运行时 `Register / Unregister / Get / Execute / ListDefinitions`
    - 支持三种工具类型：Skill、Command、MCP
    - 支持注册 Skill 声明的工具代理，`binding` 解析 `builtin:*`、`command:*`、`mcp:*`
    - _需求: 8.1, 8.2, 8.3, 8.4, 8.5_

  - [ ]* 11.2 编写工具注册/注销属性测试
    - **Property 21: 工具注册/注销往返**
    - **验证: 需求 8.2**

  - [x] 11.3 实现 HookManager
    - 创建 `internal/engine/hook_manager.go`
    - 支持 `afterTurn`、`beforePrompt`、`compact`、`tool.post_call`
    - `Register` 和 `Trigger` 线程安全
    - _需求: 8.4, 8.5_

  - [ ]* 11.4 编写钩子事件触发属性测试
    - **Property 22: 钩子事件触发**
    - **验证: 需求 8.4, 8.5**

- [x] 12. 检查点
  - 确保核心引擎和工具系统测试通过，再进入模型与 Skill 管理阶段。

- [x] 13. 模型管理与 Skill 管理
  - [x] 13.1 实现 ModelManager
    - 创建 `internal/engine/model_manager.go`
    - 供应商管理：`AddProvider`、`RemoveProvider`、`ListProviders`、`UpdateProvider`
    - 模型管理：`AddModel`、`EnableModel`、`DisableModel`、`SetDefault`、`ListModels`
    - `GetActiveLLM / GetActiveEmbedding`
    - 配置持久化到 PostgreSQL，Redis Pub/Sub 通知集群节点刷新缓存
    - _需求: 17.1, 17.2, 17.3, 17.4, 17.6, 17.7, 17.8_

  - [x] 13.2 实现 SkillManager
    - 创建 `internal/engine/skill_manager.go`
    - 统一导入格式：`SkillDocument{name, description, body, tools[]}`
    - 实现 `Add`、`Remove`、`Enable`、`Disable`、`List`、`Info`、`LoadCatalog`、`LoadBody`
    - Skill 存储在 PostgreSQL `skills` 表中，作为全局共享数据
    - 导入或更新时预计算 `name + description` 的 `catalog_embedding`
    - `LoadCatalog` 在无启用 Skill 时返回空集合而不是错误
    - 支持 `temp_file_id` 导入临时上传文件或压缩包
    - `wait=false` 时创建 `skill_import` 任务并异步完成预处理
    - 状态变更时自动注册/注销关联工具
    - Redis Pub/Sub 通知集群节点刷新缓存
    - _需求: 18.1, 18.2, 18.3, 18.4, 18.5, 18.6, 18.7, 18.8, 18.9, 18.10, 18.11, 18.13, 18.15, 18.16, 18.17, 20.2_

  - [ ]* 13.3 编写全局 Skill 持久化往返属性测试
    - **Property 23: 全局 Skill 持久化往返**
    - **验证: 需求 18.2, 18.3**
    - 覆盖场景：`name`、`description`、`body` 和 `tools[]` 导入后可等价读回

- [x] 14. 审计日志与 Token 用量
  - [x] 14.1 实现审计日志模块
    - 创建 `internal/log/audit.go`
    - 记录管理员操作、API Key 生成/吊销、会话删除、记忆删除
    - 审计日志与普通运行日志分离
    - _需求: 12.5, 12.6_

  - [ ]* 14.2 编写审计日志属性测试
    - **Property 30: 审计日志路由与完整性**
    - **验证: 需求 12.5, 12.6**

  - [x] 14.3 实现 Token 用量审计
    - 创建 `internal/log/token_audit.go`
    - 记录每次 LLM/Embedding 调用的 token 消耗
    - 支持按 `tenant_id`、时间范围、调用类型聚合查询
    - _需求: 12.10, 12.11_

  - [ ]* 14.4 编写 Token 审计属性测试
    - **Property 31: Token 审计持久化与查询**
    - **验证: 需求 12.10, 12.11, 12.12**

  - [x] 14.5 实现 Operation Telemetry 汇总器
    - 创建 `internal/log/telemetry.go`
    - 支持从请求上下文汇总 `operation`、`duration_ms`、`tokens`、`vector`、`skill`、`compact`、`profile`、`errors`
    - 仅在请求显式开启 telemetry 时输出
    - _需求: 12.14, 12.15, 13.10, 20.7_

  - [ ]* 14.6 编写 telemetry 按需返回属性测试
    - **Property 31.5: Telemetry 按需返回**
    - **验证: 需求 12.14, 12.15, 13.10, 20.7**
    - 覆盖场景：默认不返回 telemetry、显式开启时返回紧凑 summary、不适用分组省略

- [x] 15. 分布式集群支持
  - [x] 15.1 实现一致性哈希与会话亲和性
    - 创建 `internal/cluster/hash.go`
    - 基于 `session_id` 的一致性哈希，用作会话亲和性优化
    - _需求: 9.2_

  - [ ]* 15.2 编写一致性哈希属性测试
    - **Property 25: 一致性哈希确定性**
    - **验证: 需求 9.2**

  - [x] 15.3 实现数据对账机制
    - 创建 `internal/cluster/reconcile.go`
    - 实现 Redis <-> PostgreSQL 数据对账和修复
    - _需求: 9.7_

  - [ ]* 15.4 编写数据对账属性测试
    - **Property 26: 数据对账修复一致性**
    - **验证: 需求 9.7**

  - [ ]* 15.5 编写 WriteRedisFirst 最终一致性属性测试
    - **Property 27: WriteRedisFirst 最终一致性**
    - **验证: 需求 10.1, 10.5**

  - [x] 15.6 实现优雅关闭
    - 创建 `internal/cluster/shutdown.go`
    - 关闭流程：停止接收新请求 -> 刷新缓冲区 -> 释放分布式锁 -> 退出
    - CompactProcessor 在关闭时遍历未提交缓冲区执行提交
    - _需求: 9.5, 3.8_

  - [x] 15.7 实现 TaskTracker
    - 创建 `internal/engine/task_tracker.go`
    - 支持 `Create`、`Start`、`Complete`、`Fail`、`Get`、`QueueStats`
    - 任务状态机严格单调：`pending -> running -> completed/failed`
    - v1 追踪 `compact`、`skill_import`
    - _需求: 20.1, 20.2, 20.3_

  - [ ]* 15.8 编写任务状态单调属性测试
    - **Property 35.5: 任务状态单调推进**
    - **验证: 需求 20.1, 20.2, 20.3**

- [x] 16. 检查点
  - 确保分布式、审计和 Skill 管理相关测试通过，再进入 API 和接入层阶段。

- [x] 17. HTTP API 服务
  - [x] 17.1 实现业务 API 端点
    - 创建 `internal/api/router.go` 和 `internal/api/handlers.go`
    - 端点：
      - `POST /api/v1/context/assemble`
      - `POST /api/v1/context/ingest`
      - `CRUD /api/v1/sessions`
      - `POST /api/v1/memory/search`
      - `POST /api/v1/memory/store`
      - `DELETE /api/v1/memory/:id`
      - `POST /api/v1/tools/execute`
      - `GET /api/v1/tasks/:task_id`
      - `POST /api/v1/uploads/temp`
    - 使用强类型请求结构：
      - `AssembleRequest{session_id, query, token_budget?}`
      - `IngestRequest{session_id, messages[], used_contexts?, used_skills?, tool_calls?, telemetry?}`
      - `MemorySearchRequest{query, limit?, telemetry?}`
      - `MemoryStoreRequest{content, category?, metadata?, telemetry?}`
      - `ToolExecuteRequest{session_id, tool_name, params, telemetry?}`
    - 对请求体字段做最小校验并返回 400
    - 每个响应包含 `X-Request-Id` 和 `X-Process-Time`
    - `telemetry=true` 或 `telemetry.summary=true` 时返回 `OperationTelemetry`
    - `GET /api/v1/tasks/:task_id` 返回 `TaskGetResponse`
    - `POST /api/v1/uploads/temp` 返回 `temp_file_id`，上传文件写入受控临时目录并带 TTL 清理
    - _需求: 13.1, 13.2, 13.4, 13.5, 13.9, 13.10, 14.1, 18.15, 18.16, 20.3, 20.7_

  - [ ]* 17.2 编写 API 响应追踪 ID 属性测试
    - **Property 33: API 响应追踪 ID**
    - **验证: 需求 13.9**

  - [ ]* 17.2.1 编写处理耗时响应头属性测试
    - **Property 33.5: API 处理耗时头**
    - **验证: 需求 12.16**
    - 覆盖场景：所有 HTTP 响应包含非空 `X-Process-Time`

  - [x] 17.3 实现管理 API 端点
    - 创建 `internal/api/admin_handlers.go`
    - 管理认证端点：`POST /api/v1/auth/setup`、`POST /api/v1/auth/login`、`POST /api/v1/auth/verify`
    - 管理资源端点：`CRUD /api/v1/admin/users`、`CRUD /api/v1/admin/apikeys`、`CRUD /api/v1/admin/models`、`CRUD /api/v1/admin/providers`、`CRUD /api/v1/skills`、`CRUD /api/v1/webhooks`
    - Observer 端点：`GET /api/v1/observer/system`、`GET /api/v1/observer/queue`
    - Token 用量查询：`GET /api/v1/usage/tokens`
    - `/api/v1/auth/setup` 仅在系统无管理员时成功，否则返回 409
    - `/api/v1/auth/verify` 返回 `AuthVerifyResponse`
    - `/api/v1/admin/users` v1 仅实现 `list`、`create`、`update-password`、`disable`
    - _需求: 4.10, 4.11, 4.12, 13.6, 13.7, 13.8, 17.5, 18.12, 19.2, 12.12, 14.4, 20.4_

  - [x] 17.4 实现健康检查端点
    - 实现 `GET /healthz` 和 `GET /readyz`
    - `/healthz` 保持免认证
    - `/readyz` 检查 PostgreSQL 和 Redis 连接状态
    - _需求: 9.6, 20.5_

  - [x] 17.5 实现 Prometheus 指标端点
    - 实现 `GET /metrics`
    - 集成 OpenTelemetry 为关键操作生成 Span
    - _需求: 12.7, 12.8_

  - [x] 17.6 实现慢查询告警
    - 当上下文组装耗时超过 `slow_query_ms` 阈值时记录警告日志
    - _需求: 12.9_

  - [ ]* 17.7 编写慢查询告警属性测试
    - **Property 32: 慢查询告警**
    - **验证: 需求 12.9**

- [x] 18. Agent 接入层：Go SDK、MCP Server、Webhook
  - [x] 18.1 实现 Go SDK
    - 创建 `internal/sdk/engine.go`
    - 支持嵌入模式和远程模式
    - 初始化时传入 `api_key`，每次调用通过参数传入 `tenant_id`、`user_id`、`session_id`
    - _需求: 14.2_

  - [x] 18.2 实现 MCP Server
    - 创建 `internal/mcp/server.go`
    - 暴露工具：`context_assemble`、`memory_search`、`memory_store`、`memory_forget`、`session_summary`
    - 每个工具 `inputSchema` 包含 `tenant_id`、`user_id`、`session_id`
    - 启动参数 `--api-key` 传入服务认证
    - _需求: 14.3_

  - [ ]* 18.3 编写多接入方式一致性属性测试
    - **Property 34: 多接入方式行为一致性**
    - **验证: 需求 14.6**

  - [x] 18.4 实现 Webhook 事件通知
    - 创建 `internal/webhook/notifier.go`
    - 支持事件：`compact.completed`、`memory.extracted`、`session.expired`
    - 回调 URL 注册/注销，订阅信息持久化到 PostgreSQL
    - 统一事件载荷：`id`、`type`、`tenant_id`、`user_id`、`session_id`、`occurred_at`、`payload`
    - 附带头：`X-ContextOS-Event`、`X-ContextOS-Delivery-ID`
    - v1 不强制要求签名校验
    - POST 投递，失败重试 3 次
    - _需求: 14.4, 14.5_

  - [ ]* 18.5 编写 Webhook 投递属性测试
    - **Property 35: Webhook 事件投递**
    - **验证: 需求 14.4, 14.5**

- [x] 19. 检查点
  - 确保 API 和接入层测试通过，再进入 CLI 和扩展后端阶段。

- [x] 20. CLI 命令行工具
  - [x] 20.1 实现 CLI 框架与入口
    - 创建 `cmd/ctx/main.go` 和 `internal/cli/root.go`
    - 支持全局标志：`--config`、`--output`、`--tenant`、`--user`
    - 单次命令模式：`ctx serve`、`ctx migrate up`、`ctx version`、`ctx doctor`
    - 配置文件默认 `~/.ctx/config.yaml`
    - _需求: 4.1, 4.6, 4.7_

  - [x] 20.2 实现交互式 REPL 模式
    - 创建 `internal/cli/repl.go`
    - 使用 `github.com/chzyer/readline` 或 `github.com/c-bata/go-prompt`
    - 支持 Tab 自动补全、上下箭头历史记录
    - 启动时用配置中的管理员账号密码自动登录
    - 自动登录失败时提示重新输入管理员账号密码
    - _需求: 4.4, 4.7, 4.8, 4.9, 4.11, 4.13_

  - [x] 20.3 实现 REPL 斜杠命令
    - 实现 `/admin`、`/provider`、`/model`、`/skill`、`/session`、`/memory`、`/search`、`/apikey`、`/migrate`、`/logs`、`/status`、`/help`、`/logout`、`/exit`
    - `/help` 展示所有命令列表，`/help <命令>` 展示详细用法
    - `/status` 支持查看 observer/system、observer/queue 和任务状态
    - 输出格式支持 `table` 和 `json`
    - _需求: 4.5, 4.10, 4.11, 4.12_

  - [ ]* 20.4 编写 CLI 输出格式属性测试
    - **Property 36: CLI 输出格式正确性**
    - **验证: 需求 4.12**

  - [x] 20.5 实现 `ctx serve`
    - 启动流程：加载配置 -> 连接 PG -> 连接 Redis -> 自动迁移 -> 初始化默认管理员 -> 初始化 VectorStore -> 初始化核心引擎 -> 数据对账 -> 启动 HTTP 服务
    - 注册优雅关闭信号处理
    - _需求: 4.3, 16.1, 16.6, 9.5, 9.6, 9.7_

  - [x] 20.6 实现 `ctx migrate`
    - 子命令：`up`、`down`、`status`
    - 支持 `--dsn`
    - _需求: 16.4_

  - [x] 20.7 实现 `ctx doctor`
    - 创建 `internal/cli/doctor.go`
    - 检查配置文件、PostgreSQL、Redis、模型配置、磁盘空间和关键依赖可用性
    - 输出可执行的修复建议
    - _需求: 20.6_

- [x] 21. 向量存储扩展后端
  - [x] 21.1 实现 Elasticsearch 向量存储后端
    - 创建 `internal/store/es_store.go`
    - _需求: 6.2_

  - [x] 21.2 实现 Milvus 向量存储后端
    - 创建 `internal/store/milvus_store.go`
    - _需求: 6.2_

- [x] 22. 最终检查点
  - 运行全量测试和文档中要求的属性测试。
  - 核对业务 API、管理 API、CLI 命令、认证边界、Skill 渐进式加载和隔离规则是否全部实现。

## 备注

- 标记 `*` 的任务为测试任务，建议与对应实现同阶段完成
- 每个任务引用了具体需求编号，属性测试编号与 `design.md` 一致
- 实施时优先保持当前架构和阶段推进逻辑，不做无关重构
