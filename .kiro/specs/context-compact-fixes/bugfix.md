# 缺陷修复需求文档

## 简介

本文档涵盖上下文管理与压缩（Compact）系统中的多个关键缺陷，分为两大类：**A 类 — 基础设施与可靠性问题**（超时缺失、竞态条件、panic recovery、DLQ 泄漏、输入校验等）和 **B 类 — 上下文管理与压缩逻辑问题**（压缩无效、快照覆盖、Token 估算、UTF-8 截断、角色丢失等）。这些缺陷分布在 `compact_processor.go`、`context_builder.go`、`retrieval.go`、`session_manager.go`、`sync_queue.go`、`handlers.go`、`shutdown.go` 等文件中。如不修复，将导致资源耗尽、数据损坏、内存泄漏、用户画像丢失、多语言场景下 LLM 调用超限、上下文质量严重退化等后果。

## 缺陷分析

### 当前行为（缺陷）

**A 类：基础设施与可靠性**

1.1 WHEN 外部服务（LLM、embedding、向量存储）调用执行时 THEN 系统未对这些调用设置 context timeout，如果外部服务挂起，请求将无限阻塞，导致 goroutine 泄漏和资源耗尽

1.2 WHEN `SessionManager.AddMessage` 在锁内修改 `session.Messages` 后释放锁 THEN 系统继续使用同一个 session 对象调用 `putRedis()` 和 `syncQueue.Enqueue()`，此时其他 goroutine 可能并发读写该 session，导致数据竞争和消息损坏

1.3 WHEN `compact_processor.go` 中 `EvaluateAndTrigger` 启动的 goroutine 发生 panic 时 THEN 系统没有 panic recovery 机制，goroutine 静默崩溃，semaphore slot 和 activeLock 永远不会释放，导致后续压缩被永久阻塞

1.4 WHEN `SyncQueue.flush` 失败并将 item 发送到 DLQ 时 THEN DLQ channel 永远不会被消费或清理，失败的 item 持续累积，最终导致 DLQ 满后新的失败 item 被静默丢弃，且内存持续增长

1.5 WHEN `GracefulShutdown.Shutdown` 执行时 THEN 系统不会等待正在执行的 compact goroutine 完成，直接关闭，可能导致压缩操作中途中断、checkpoint 未持久化、分布式锁未释放

1.6 WHEN API handler 接收请求参数时 THEN 系统未校验 `req.TokenBudget`（可为负数）、`req.TopK`（可为 0 或负数）、`req.Messages` 数组（无大小限制），可被利用进行 DoS 攻击或触发意外行为

**B 类：上下文管理与压缩逻辑**

1.7 WHEN `executeCompact` 执行完成后 THEN 系统仅更新 session 的 metadata（`last_compact_turn`、`last_compact_tokens`），但从未删除或替换原始消息，导致 `totalTokens` 始终基于全部消息计算，Token 数只增不减，压缩实际上是空操作

1.8 WHEN 创建 `CompactCheckpoint` 时 THEN 系统将 `SourceTurnStart` 硬编码为 0，导致每次压缩都从会话开头开始摘要，而非仅处理上次压缩之后的新消息，随着会话增长 LLM 提示词线性增大，增加成本和延迟

1.9 WHEN `extractFacts` 处理 LLM 生成的摘要文本时 THEN 系统仅按句末标点（`.!?。！？`）拆分，无法正确处理列表、编号项、段落等结构，产生碎片化的无意义"事实"，污染向量存储的语义搜索质量

1.10 WHEN `mergeProfile` 合并用户画像时 THEN 系统直接执行 `existing.Summary = summary` 覆盖旧摘要，而非合并，导致多会话场景下后续压缩完全丢失早期会话的摘要信息

1.11 WHEN `mergeProfile` 提取用户偏好时 THEN 系统使用 `strings.Contains(lower, "prefer")` 和 `strings.Contains(lower, "like")` 进行简单子串匹配，导致 "I don't like X" 被错误存储为偏好，"likewise" 也会触发匹配，产生不正确的用户画像数据

1.12 WHEN `EvaluateAndTrigger` 触发压缩时 THEN 系统克隆会话为快照，在快照上执行压缩，然后通过 `p.sessions.store.Save(ctx, snapshot)` 将快照以原始 session ID 保存回存储，覆盖了压缩期间可能已更新的实际会话最新状态（包括新增消息），导致数据丢失

1.13 WHEN `estimateTokens` 处理非英文文本（如中文、日文、韩文）时 THEN 系统使用 `len(s)/4` 按字节长度估算 Token 数，但 CJK 字符在 UTF-8 中占 3 字节，被估算为 0.75 个 Token，实际为 1-2 个 Token，导致发送给 LLM 的实际 Token 数超出预算

1.14 WHEN `matchAndLoadSkills` 对技能目录进行语义匹配时 THEN 系统对每个技能单独调用 `b.embedding.Embed()`，50 个技能就产生 50 次嵌入 API 调用，造成不可接受的延迟和成本

1.15 WHEN `ContextBuilder.Assemble` 分配记忆搜索预算时 THEN 系统硬编码 `memBudget := budget / 4`，不考虑实际记忆量或会话历史长度，当用户有丰富记忆但会话较短时，该分配方式造成浪费

1.16 WHEN `demoteBlock` 和 `applyContentLevel` 截断内容时 THEN 系统使用 `content[:200]` 和 `content[:1000]` 按字节截断，对于多字节字符（中文等）可能在字符中间截断，产生无效的 UTF-8 字符串

1.17 WHEN `buildMessages` 构建历史消息时 THEN 系统将所有历史块的角色设置为 `Role: "user"`，丢失了原始的 assistant/system 角色信息，LLM 将所有对话历史视为用户消息，严重影响上下文理解

1.18 WHEN 渐进式加载过程中 L0 或 L1 级别的块超出预算时 THEN 系统直接跳过该块而不尝试截断，但记忆块为 L0 级别，如果单个记忆较大，要么完整加入要么完全排除，没有中间方案

### 期望行为（正确）

**A 类：基础设施与可靠性**

2.1 WHEN 外部服务（LLM、embedding、向量存储）调用执行时 THEN 系统 SHALL 为每个调用设置合理的 context timeout（可通过配置调整），超时后自动取消请求并返回错误，防止 goroutine 无限阻塞

2.2 WHEN `SessionManager.AddMessage` 修改 session 后需要写入 Redis 和 SyncQueue 时 THEN 系统 SHALL 在锁内完成 session 数据的深拷贝，将拷贝传递给 `putRedis()` 和 `syncQueue.Enqueue()`，确保锁外操作不会与并发读写产生数据竞争

2.3 WHEN `compact_processor.go` 中启动的 goroutine 执行时 THEN 系统 SHALL 在 goroutine 入口处添加 `defer recover()` 进行 panic recovery，确保 panic 时 semaphore slot 和 activeLock 被正确释放，并记录错误日志

2.4 WHEN `SyncQueue.flush` 失败并将 item 发送到 DLQ 时 THEN 系统 SHALL 提供 DLQ 消费机制（定期重试或导出），并在 DLQ 接近满时记录告警日志，防止内存无限增长和失败 item 被静默丢弃

2.5 WHEN `GracefulShutdown.Shutdown` 执行时 THEN 系统 SHALL 等待所有正在执行的 compact goroutine 完成（带超时），确保 checkpoint 持久化和分布式锁释放后再退出

2.6 WHEN API handler 接收请求参数时 THEN 系统 SHALL 校验 `req.TokenBudget`（必须 > 0 或为空使用默认值）、`req.TopK`（必须 > 0）、`req.Messages` 数组（设置合理的大小上限），对非法值返回 400 错误

**B 类：上下文管理与压缩逻辑**

2.7 WHEN `executeCompact` 执行完成后 THEN 系统 SHALL 用摘要消息替换已压缩的旧消息，仅保留摘要和最近 N 条原始消息（N 由 `RecentRawTurnCount` 配置），使后续 `totalTokens` 计算反映压缩后的实际消息量

2.8 WHEN 创建 `CompactCheckpoint` 时 THEN 系统 SHALL 将 `SourceTurnStart` 设置为上次压缩的 `SourceTurnEnd`（首次压缩时为 0），仅对新增消息生成摘要，避免重复处理已压缩内容

2.9 WHEN `extractFacts` 处理 LLM 摘要文本时 THEN 系统 SHALL 使用结构化解析策略，正确处理编号列表、项目符号列表和段落结构，确保每个提取的事实是完整且有意义的语义单元

2.10 WHEN `mergeProfile` 合并用户画像时 THEN 系统 SHALL 将新摘要与旧摘要合并（如拼接或使用 LLM 融合），而非直接覆盖，确保跨会话的摘要信息不丢失

2.11 WHEN `mergeProfile` 提取用户偏好时 THEN 系统 SHALL 使用更精确的匹配逻辑，排除否定表达（如 "don't like"、"not prefer"）和无关子串匹配（如 "likewise"），确保仅存储真正的用户偏好

2.12 WHEN `executeCompact` 完成后需要更新会话元数据时 THEN 系统 SHALL 仅更新原始会话的 metadata 字段（`last_compact_at`、`last_compact_turn`、`last_compact_tokens`）和压缩后的消息列表，而非用快照整体覆盖原始会话，避免丢失压缩期间新增的消息

2.13 WHEN `estimateTokens` 处理包含非 ASCII 字符的文本时 THEN 系统 SHALL 使用 Unicode 感知的 Token 估算方法，对 CJK 等多字节字符按 rune 计数并应用适当的 Token/rune 比率，确保估算值不低于实际 Token 数

2.14 WHEN `matchAndLoadSkills` 对技能目录进行语义匹配时 THEN 系统 SHALL 批量收集所有技能描述文本，通过单次 `Embed()` 调用获取所有嵌入向量，将 N 次 API 调用减少为 1 次

2.15 WHEN `ContextBuilder.Assemble` 分配记忆搜索预算时 THEN 系统 SHALL 根据实际可用的记忆量和会话历史长度动态调整记忆预算比例，而非使用固定的 1/4 分配

2.16 WHEN `demoteBlock` 和 `applyContentLevel` 截断内容时 THEN 系统 SHALL 按 rune（Unicode 码点）而非字节进行截断，确保截断后的字符串始终是有效的 UTF-8

2.17 WHEN `buildMessages` 构建历史消息时 THEN 系统 SHALL 从历史块的内容中解析并保留原始消息角色（user/assistant/system），确保 LLM 接收到正确的对话角色信息

2.18 WHEN 渐进式加载过程中任何级别的块超出预算时 THEN 系统 SHALL 对所有级别的块（包括 L0 和 L1）尝试截断以适应剩余预算，而非仅对 L2 块进行降级处理

### 不变行为（回归防护）

3.1 WHEN 会话消息数未达到任何压缩触发条件时 THEN 系统 SHALL CONTINUE TO 不触发压缩，`shouldTrigger` 的四个触发条件逻辑保持不变

3.2 WHEN 压缩正常完成时 THEN 系统 SHALL CONTINUE TO 生成 `CompactCheckpoint` 并持久化，触发 hooks 和 webhooks 通知，记录 Token 审计日志

3.3 WHEN 向量存储或嵌入服务不可用时 THEN 系统 SHALL CONTINUE TO 允许压缩的其余步骤（摘要生成、画像合并等）继续执行，不因记忆存储失败而回滚整个压缩

3.4 WHEN `ContextBuilder.Assemble` 处理纯英文文本时 THEN 系统 SHALL CONTINUE TO 产生与当前 `len(s)/4` 近似一致的 Token 估算结果

3.5 WHEN 技能目录为空时 THEN 系统 SHALL CONTINUE TO 跳过技能匹配阶段，不调用嵌入 API

3.6 WHEN 用户画像不存在时 THEN 系统 SHALL CONTINUE TO 创建新的空画像并填充当前会话的信息

3.7 WHEN `SessionManager.AddMessage` 添加消息时 THEN 系统 SHALL CONTINUE TO 正确执行三层缓存写入（LRU → Redis → PG 异步队列）和 `MaxMessages` 裁剪逻辑

3.8 WHEN 渐进式加载中所有块均在预算内时 THEN 系统 SHALL CONTINUE TO 按分数降序排列并完整包含所有块，不进行不必要的截断

3.9 WHEN 分布式锁已被其他实例持有时 THEN 系统 SHALL CONTINUE TO 跳过本次压缩而非阻塞等待，保持当前的非阻塞锁获取行为

3.10 WHEN 语义搜索返回结果时 THEN 系统 SHALL CONTINUE TO 按相似度分数降序排列，并在预算内进行截断

3.11 WHEN `SyncQueue.Enqueue` 在队列已停止时被调用 THEN 系统 SHALL CONTINUE TO 返回错误而非阻塞

3.12 WHEN 合法的 API 请求（参数在有效范围内）到达时 THEN 系统 SHALL CONTINUE TO 正常处理请求，新增的输入校验不影响合法请求的处理
