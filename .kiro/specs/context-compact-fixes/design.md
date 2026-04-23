# 上下文压缩系统缺陷修复设计

## 概述

本设计文档针对上下文管理与压缩（Compact）系统中的 18 个缺陷，提供系统化的修复方案。缺陷分为两大类：A 类（基础设施与可靠性，1.1-1.6）和 B 类（上下文管理与压缩逻辑，1.7-1.18）。修复策略遵循最小变更原则，确保每个修复精准定位根因，同时通过保持性检查（Preservation Checking）防止回归。

## 术语表

- **Bug_Condition (C)**: 触发缺陷的输入条件，例如外部服务调用无 timeout、并发写入同一 session 对象
- **Property (P)**: 缺陷修复后的期望行为，例如超时后自动取消、深拷贝后无数据竞争
- **Preservation**: 修复不应改变的现有行为，例如 `shouldTrigger` 四条件逻辑、三层缓存写入顺序
- **CompactProcessor**: `internal/engine/compact_processor.go` 中的压缩处理器，负责异步会话压缩
- **ContextBuilder**: `internal/engine/context_builder.go` 中的上下文构建器，负责两阶段上下文组装
- **SessionManager**: `internal/engine/session_manager.go` 中的会话管理器，实现三层缓存（LRU → Redis → PG）
- **SyncQueue**: `internal/engine/sync_queue.go` 中的异步写入队列，批量持久化到 PostgreSQL
- **estimateTokens**: `internal/engine/retrieval.go` 中的 Token 估算函数，当前使用 `len(s)/4`
- **GracefulShutdown**: `internal/cluster/shutdown.go` 中的优雅关闭协调器

## 缺陷详情

### Bug Condition

系统存在 18 个缺陷，涵盖基础设施可靠性和压缩逻辑两大领域。缺陷的核心触发条件可归纳为以下复合条件：

**形式化规约：**
```
FUNCTION isBugCondition(input)
  INPUT: input of type SystemOperation
  OUTPUT: boolean

  // A 类：基础设施缺陷
  A1 := input.type == "external_call" AND input.context HAS NO timeout
  A2 := input.type == "add_message" AND input.session IS shared_reference AND concurrent_access(input.session)
  A3 := input.type == "compact_goroutine" AND input.execution CAUSES panic
  A4 := input.type == "sync_flush_failure" AND dlq.length > 0 AND NO dlq_consumer_exists
  A5 := input.type == "shutdown" AND active_compact_goroutines > 0
  A6 := input.type == "api_request" AND (input.tokenBudget < 0 OR input.topK <= 0 OR len(input.messages) > MAX_LIMIT)

  // B 类：压缩逻辑缺陷
  B7 := input.type == "compact_complete" AND original_messages NOT replaced
  B8 := input.type == "compact_checkpoint" AND sourceTurnStart == 0 ALWAYS
  B9 := input.type == "extract_facts" AND input.summary CONTAINS structured_content (lists, numbered_items)
  B10 := input.type == "merge_profile" AND existing.Summary != "" AND action == "overwrite"
  B11 := input.type == "merge_profile" AND input.fact CONTAINS negation ("don't like", "not prefer") OR false_match ("likewise")
  B12 := input.type == "compact_save" AND snapshot OVERWRITES live_session WITH stale_data
  B13 := input.type == "estimate_tokens" AND input.text CONTAINS CJK_characters
  B14 := input.type == "skill_match" AND len(catalog) > 1 AND embedding_calls == len(catalog)
  B15 := input.type == "assemble" AND memBudget == budget/4 ALWAYS (hardcoded)
  B16 := input.type == "truncate_content" AND input.content CONTAINS multibyte_chars AND truncation_at_byte_boundary
  B17 := input.type == "build_messages" AND history_block.original_role != "user" AND output.role == "user"
  B18 := input.type == "progressive_load" AND block.level IN [L0, L1] AND block.tokens > remaining_budget AND NO truncation_attempted

  RETURN A1 OR A2 OR A3 OR A4 OR A5 OR A6
         OR B7 OR B8 OR B9 OR B10 OR B11 OR B12
         OR B13 OR B14 OR B15 OR B16 OR B17 OR B18
END FUNCTION
```

### 示例

- **1.1 超时缺失**: `p.llm.Complete(ctx, ...)` 使用无 deadline 的 `context.Background()`，LLM 服务挂起时 goroutine 永久阻塞
- **1.2 竞态条件**: `AddMessage` 在 `mu.Unlock()` 后使用同一 `session` 指针调用 `putRedis()`，另一 goroutine 同时调用 `AddMessage` 修改 `session.Messages`
- **1.7 压缩无效**: `executeCompact` 完成后仅更新 metadata，`session.Messages` 保持原样，`totalTokens` 永远递增
- **1.8 SourceTurnStart=0**: 每次压缩 `SourceTurnStart: 0` 导致从头摘要，第 100 次压缩仍处理全部 100 条消息
- **1.12 快照覆盖**: `p.sessions.store.Save(ctx, snapshot)` 将克隆快照（含旧 session ID）写回存储，覆盖压缩期间新增的消息
- **1.13 CJK Token 估算**: "你好世界"（12 字节 UTF-8）被估算为 3 Token，实际约 4-8 Token
- **1.16 UTF-8 截断**: `content[:200]` 对 "你好..." 可能在 3 字节 CJK 字符中间截断，产生无效 UTF-8
- **1.17 角色丢失**: `buildMessages` 将所有历史块设为 `Role: "user"`，assistant 回复也变成 user 消息

## 期望行为

### 保持性要求

**不变行为：**
- `shouldTrigger` 的四个触发条件（Token 比率、新增 Token 阈值、Turn 阈值、时间间隔）逻辑保持不变
- 压缩正常完成时继续生成 `CompactCheckpoint`、触发 hooks/webhooks、记录 Token 审计
- 向量存储或嵌入服务不可用时，压缩其余步骤（摘要、画像合并）继续执行
- 纯英文文本的 Token 估算结果与 `len(s)/4` 近似一致
- 技能目录为空时跳过匹配，不调用嵌入 API
- 用户画像不存在时创建新空画像
- `AddMessage` 三层缓存写入（LRU → Redis → PG 异步）和 `MaxMessages` 裁剪逻辑不变
- 渐进式加载中所有块在预算内时按分数降序完整包含
- 分布式锁已被持有时跳过压缩（非阻塞）
- 语义搜索按相似度降序排列并在预算内截断
- `SyncQueue.Enqueue` 在队列停止时返回错误
- 合法 API 请求正常处理

**范围：**
所有不涉及上述 18 个缺陷触发条件的输入应完全不受修复影响。

## 假设根因分析

### A 类：基础设施与可靠性

1. **1.1 Context Timeout 缺失**: `executeCompact` 中 `p.llm.Complete(ctx, ...)` 和 `p.embedding.Embed(ctx, ...)` 使用 `context.Background()` 创建的 ctx，无 deadline。根因：goroutine 入口处 `compactCtx := context.Background()` 未设置超时。

2. **1.2 SessionManager 竞态条件**: `AddMessage` 在 `m.mu.Unlock()` 后直接使用 `session` 指针调用 `putRedis` 和 `syncQueue.Enqueue`，而 `session` 是共享引用。根因：锁保护范围不足，锁外操作使用了共享可变状态。

3. **1.3 Compact Goroutine 无 Panic Recovery**: `EvaluateAndTrigger` 中 `go func() { ... }()` 无 `defer recover()`。根因：goroutine 内 `defer` 仅在 `executeCompact` 内部，panic 发生在 `executeCompact` 调用之前（如 `p.tasks.Start`）时无法捕获。

4. **1.4 DLQ 无消费机制**: `SyncQueue.flush` 失败时写入 `q.dlq` channel，但无任何 goroutine 消费该 channel。根因：DLQ 设计不完整，仅有写入端无读取端。

5. **1.5 Shutdown 不等待 Compact**: `GracefulShutdown.Shutdown` 调用 `flush()` 但 `CompactProcessor` 无 `WaitGroup` 或类似机制追踪活跃 goroutine。根因：`compactFlush` 回调为空操作或仅清理缓冲区，不等待 goroutine。

6. **1.6 API 输入无校验**: `handleAssemble` 和 `handleIngest` 仅校验 `Query` 和 `Messages` 非空，未校验 `TokenBudget`、`TopK` 等数值范围。根因：handler 层缺少数值范围校验。

### B 类：上下文管理与压缩逻辑

7. **1.7 压缩不减少消息**: `executeCompact` 完成后仅更新 `snapshot.Metadata`，从未修改 `snapshot.Messages`（如替换为摘要 + 最近 N 条）。根因：缺少消息替换逻辑。

8. **1.8 SourceTurnStart 硬编码**: `compact_processor.go` 第 ~230 行 `SourceTurnStart: 0` 硬编码。根因：未从 session metadata 的 `last_compact_turn` 读取上次压缩位置。

9. **1.9 事实提取粗糙**: `extractFacts` 仅按 `.!?。！？` 拆分句子。根因：未处理 Markdown 列表、编号项、段落等结构化格式。

10. **1.10 Profile Summary 覆盖**: `mergeProfile` 中 `existing.Summary = summary` 直接赋值。根因：缺少新旧摘要合并逻辑。

11. **1.11 偏好提取误判**: `strings.Contains(lower, "prefer")` 和 `strings.Contains(lower, "like")` 无法区分肯定/否定语境。根因：简单子串匹配无语义理解。

12. **1.12 快照覆盖活跃会话**: `p.sessions.store.Save(ctx, snapshot)` 将整个快照（含 `snapshot.ID = session.ID`）写回存储。根因：应仅原子更新 metadata 字段，而非整体覆盖。

13. **1.13 CJK Token 估算偏低**: `estimateTokens` 使用 `len(s)/4`（字节数/4），CJK 字符 3 字节被估为 0.75 Token。根因：未区分 ASCII 和多字节字符。

14. **1.14 N+1 嵌入调用**: `matchAndLoadSkills` 对每个技能单独调用 `b.embedding.Embed(ctx, []string{skillText})`。根因：循环内逐个调用而非批量。

15. **1.15 硬编码记忆预算**: `memBudget := budget / 4` 固定分配 25%。根因：未考虑实际记忆量和会话长度。

16. **1.16 UTF-8 字节截断**: `demoteBlock` 和 `applyContentLevel` 使用 `content[:200]` 和 `content[:1000]` 按字节索引截断。根因：Go 字符串索引是字节偏移，非 rune 偏移。

17. **1.17 历史角色丢失**: `buildMessages` 将所有 history 块设为 `Role: "user"`。根因：`ContentBlock` 未保存原始角色，构建时也未从 Content 中解析。

18. **1.18 渐进式加载仅降级 L2**: progressive loading 循环中 `if blk.Level == types.ContentL2` 条件限制了仅 L2 块可降级。根因：L0/L1 块超预算时直接跳过，无截断尝试。

## 正确性属性

Property 1: Bug Condition - 外部调用超时保护 (1.1)

_For any_ 外部服务调用（LLM Complete、Embedding Embed、VectorStore 操作），修复后的代码 SHALL 使用带有可配置 timeout 的 `context.WithTimeout` 包装调用上下文，超时后自动取消并返回错误。

**Validates: Requirements 2.1**

Property 2: Bug Condition - SessionManager 并发安全 (1.2)

_For any_ 并发调用 `AddMessage` 的场景，修复后的 `AddMessage` SHALL 在锁内完成 session 数据的深拷贝，锁外操作（`putRedis`、`syncQueue.Enqueue`）使用拷贝而非原始引用，消除数据竞争。

**Validates: Requirements 2.2**

Property 3: Bug Condition - Compact Goroutine Panic Recovery (1.3)

_For any_ compact goroutine 执行过程中发生的 panic，修复后的代码 SHALL 通过 `defer recover()` 捕获 panic，正确释放 semaphore slot 和 activeLock，并记录错误日志。

**Validates: Requirements 2.3**

Property 4: Bug Condition - DLQ 消费机制 (1.4)

_For any_ 进入 DLQ 的失败 item，修复后的系统 SHALL 提供定期重试消费机制，并在 DLQ 接近满时记录告警日志。

**Validates: Requirements 2.4**

Property 5: Bug Condition - Shutdown 等待 Compact (1.5)

_For any_ 正在执行的 compact goroutine，修复后的 `GracefulShutdown.Shutdown` SHALL 等待所有活跃 compact goroutine 完成（带超时），确保 checkpoint 持久化和锁释放。

**Validates: Requirements 2.5**

Property 6: Bug Condition - API 输入校验 (1.6)

_For any_ API 请求中 `TokenBudget < 0`、`TopK <= 0` 或 `Messages` 数组超过上限的情况，修复后的 handler SHALL 返回 HTTP 400 错误。

**Validates: Requirements 2.6**

Property 7: Bug Condition - 压缩实际减少消息 (1.7)

_For any_ 成功完成的压缩操作，修复后的 `executeCompact` SHALL 用摘要消息替换已压缩的旧消息，仅保留摘要 + 最近 `RecentRawTurnCount` 条原始消息。

**Validates: Requirements 2.7**

Property 8: Bug Condition - SourceTurnStart 增量压缩 (1.8)

_For any_ 非首次压缩，修复后的代码 SHALL 将 `SourceTurnStart` 设置为上次压缩的 `last_compact_turn`，仅对新增消息生成摘要。

**Validates: Requirements 2.8**

Property 9: Bug Condition - 结构化事实提取 (1.9)

_For any_ 包含编号列表、项目符号或段落结构的 LLM 摘要文本，修复后的 `extractFacts` SHALL 正确解析结构，每个提取的事实是完整的语义单元。

**Validates: Requirements 2.9**

Property 10: Bug Condition - Profile Summary 合并 (1.10)

_For any_ 已存在 Summary 的用户画像，修复后的 `mergeProfile` SHALL 将新摘要与旧摘要合并而非覆盖。

**Validates: Requirements 2.10**

Property 11: Bug Condition - 偏好提取精确匹配 (1.11)

_For any_ 包含否定表达（"don't like"、"not prefer"）或无关子串（"likewise"）的事实文本，修复后的偏好提取 SHALL 不将其错误归类为偏好。

**Validates: Requirements 2.11**

Property 12: Bug Condition - 原子 Metadata 更新 (1.12)

_For any_ 压缩完成后的会话更新，修复后的代码 SHALL 仅原子更新原始会话的 metadata 和压缩后消息列表，不覆盖压缩期间新增的消息。

**Validates: Requirements 2.12**

Property 13: Bug Condition - CJK Token 估算准确性 (1.13)

_For any_ 包含 CJK 字符的文本，修复后的 `estimateTokens` SHALL 使用 Unicode 感知的估算方法，估算值不低于实际 Token 数。

**Validates: Requirements 2.13**

Property 14: Bug Condition - 批量嵌入调用 (1.14)

_For any_ 包含 N 个技能的目录，修复后的 `matchAndLoadSkills` SHALL 通过单次 `Embed()` 调用获取所有嵌入向量。

**Validates: Requirements 2.14**

Property 15: Bug Condition - 动态记忆预算 (1.15)

_For any_ 上下文组装请求，修复后的 `Assemble` SHALL 根据实际记忆量和会话历史长度动态调整记忆预算比例。

**Validates: Requirements 2.15**

Property 16: Bug Condition - UTF-8 安全截断 (1.16)

_For any_ 包含多字节字符的内容截断操作，修复后的 `demoteBlock` 和 `applyContentLevel` SHALL 按 rune 而非字节截断，输出始终是有效 UTF-8。

**Validates: Requirements 2.16**

Property 17: Bug Condition - 历史消息角色保留 (1.17)

_For any_ 历史消息块，修复后的 `buildMessages` SHALL 保留原始消息角色（user/assistant/system）。

**Validates: Requirements 2.17**

Property 18: Bug Condition - 全级别渐进式截断 (1.18)

_For any_ 超出预算的 L0 或 L1 级别块，修复后的渐进式加载 SHALL 尝试截断以适应剩余预算，而非直接跳过。

**Validates: Requirements 2.18**

Property 19: Preservation - 压缩触发条件不变

_For any_ 会话消息数未达到任何压缩触发条件的输入，修复后的系统 SHALL 产生与修复前完全相同的行为，`shouldTrigger` 逻辑不变。

**Validates: Requirements 3.1, 3.2, 3.3, 3.9**

Property 20: Preservation - 纯英文 Token 估算一致性

_For any_ 仅包含 ASCII 字符的文本，修复后的 `estimateTokens` SHALL 产生与 `len(s)/4` 近似一致的结果（误差 ≤ 10%）。

**Validates: Requirements 3.4**

Property 21: Preservation - 三层缓存与合法请求处理

_For any_ 合法的 API 请求和 `AddMessage` 调用，修复后的系统 SHALL 继续正确执行三层缓存写入、`MaxMessages` 裁剪、以及正常请求处理。

**Validates: Requirements 3.7, 3.8, 3.10, 3.11, 3.12**


## 修复实现

假设根因分析正确，以下是各缺陷的具体修复方案：

### A 类：基础设施与可靠性

**1.1 — Context Timeout**

**文件**: `internal/engine/compact_processor.go`
**函数**: `executeCompact`

**具体变更**:
1. **在 CompactConfig 中添加超时配置**: 新增 `LLMTimeoutSec`、`EmbedTimeoutSec`、`VectorTimeoutSec` 字段，默认值分别为 60、30、30 秒
2. **包装 LLM 调用**: 在 `p.llm.Complete(ctx, ...)` 前使用 `context.WithTimeout(ctx, llmTimeout)` 创建子 context
3. **包装 Embedding 调用**: 在 `p.embedding.Embed(ctx, ...)` 前使用 `context.WithTimeout(ctx, embedTimeout)`
4. **包装 VectorStore 调用**: 在 `p.vectorStore.Upsert(ctx, ...)` 前使用 `context.WithTimeout(ctx, vectorTimeout)`
5. **Compact goroutine 入口超时**: 将 `compactCtx := context.Background()` 改为 `compactCtx, cancel := context.WithTimeout(context.Background(), overallTimeout)`，并 `defer cancel()`

---

**1.2 — SessionManager 竞态条件**

**文件**: `internal/engine/session_manager.go`
**函数**: `AddMessage`

**具体变更**:
1. **锁内深拷贝**: 在 `m.mu.Lock()` 块内，修改 `session.Messages` 后立即调用 `cloneSession(session)` 生成深拷贝
2. **锁外使用拷贝**: `putRedis(ctx, key, cloned)` 和 `syncQueue.Enqueue(item)` 使用拷贝而非原始 session 指针
3. **LRU 也使用拷贝**: `putLRU(key, cloned)` 确保 LRU 缓存中的引用独立

```go
// 修复后的 AddMessage 伪代码
func (m *SessionManager) AddMessage(ctx, session, msg) error {
    m.mu.Lock()
    session.Messages = append(session.Messages, msg)
    // trim MaxMessages ...
    session.UpdatedAt = time.Now()
    cloned := cloneSession(session)  // 锁内深拷贝
    m.mu.Unlock()

    key := m.cacheKey(...)
    m.putLRU(key, cloned)
    if err := m.putRedis(ctx, key, cloned); err != nil { return err }
    item := &SyncItem{..., Session: cloned}
    return m.syncQueue.Enqueue(item)
}
```

---

**1.3 — Compact Goroutine Panic Recovery**

**文件**: `internal/engine/compact_processor.go`
**函数**: `EvaluateAndTrigger`

**具体变更**:
1. **在 goroutine 入口添加 defer recover()**: 在 `go func() { ... }()` 的第一行添加 panic recovery
2. **确保资源释放**: recover 中释放 `activeLocks.Delete(session.ID)` 和 `<-p.semaphore`
3. **记录 panic 日志**: 使用 `p.logger.Error("compact goroutine panicked", ...)` 记录堆栈

```go
go func() {
    defer func() {
        if r := recover(); r != nil {
            p.activeLocks.Delete(session.ID)
            <-p.semaphore
            p.logger.Error("compact goroutine panicked",
                zap.String("session_id", session.ID),
                zap.Any("panic", r),
            )
            if taskID != "" && p.tasks != nil {
                _ = p.tasks.Fail(context.Background(), taskID, fmt.Errorf("panic: %v", r))
            }
        }
    }()
    // ... existing logic
}()
```

---

**1.4 — DLQ 消费机制**

**文件**: `internal/engine/sync_queue.go`

**具体变更**:
1. **添加 DLQ 消费 goroutine**: 新增 `dlqWorker()` 方法，定期（如每 30 秒）从 DLQ 取出 item 重试
2. **重试次数限制**: DLQ item 最多重试 3 次，超过后记录日志并丢弃
3. **DLQ 满告警**: 在 `flush` 中 DLQ 写入失败时（`default` 分支）记录 `WARN` 级别日志
4. **在 SyncItem 中添加 RetryCount 字段**: 追踪重试次数
5. **Start() 中启动 dlqWorker**: `go q.dlqWorker()`

---

**1.5 — Shutdown 等待 Compact**

**文件**: `internal/engine/compact_processor.go` + `internal/cluster/shutdown.go`

**具体变更**:
1. **在 CompactProcessor 中添加 `sync.WaitGroup`**: 追踪活跃 compact goroutine
2. **goroutine 入口 `wg.Add(1)`，出口 `defer wg.Done()`**
3. **新增 `WaitForCompletion(timeout)` 方法**: 使用 `wg.Wait()` 配合 `context.WithTimeout` 等待
4. **`GracefulShutdown.RegisterCompactFlush`**: 注册的 flush 函数调用 `CompactProcessor.WaitForCompletion()`
5. **Shutdown 中先等待 compact 完成再停止 sync queue**

---

**1.6 — API 输入校验**

**文件**: `internal/api/handlers.go`

**具体变更**:
1. **`handleAssemble` 中校验 `TokenBudget`**: 如果 `req.TokenBudget < 0`，返回 400
2. **`handleAssemble` 中校验 `TopK`**: 如果请求中包含 TopK 且 `<= 0`，返回 400
3. **`handleIngest` 中校验 `Messages` 大小**: 设置上限（如 200 条），超过返回 400
4. **`handleMemorySearch` 中校验 `Limit`**: 如果 `req.Limit < 0`，返回 400
5. **提取公共校验函数**: `validateAssembleRequest(req)` 和 `validateIngestRequest(req)` 返回 error

### B 类：上下文管理与压缩逻辑

**1.7 — 压缩实际减少消息**

**文件**: `internal/engine/compact_processor.go`
**函数**: `executeCompact`

**具体变更**:
1. **在摘要生成后替换消息**: 构建新的消息列表 = `[摘要消息] + 最近 RecentRawTurnCount 条原始消息`
2. **摘要消息格式**: `Message{Role: "system", Content: "[Compact Summary] " + summaryContent}`
3. **从 CompactConfig 读取 RecentRawTurnCount**: 新增配置字段，默认值 8
4. **更新 snapshot.Messages**: 在 `p.sessions.store.Save` 前替换消息列表
5. **重新计算 Token 数**: 基于新消息列表计算 `last_compact_tokens`

---

**1.8 — SourceTurnStart 增量压缩**

**文件**: `internal/engine/compact_processor.go`
**函数**: `executeCompact`

**具体变更**:
1. **从 session metadata 读取 `last_compact_turn`**: 作为 `SourceTurnStart`
2. **仅对新消息生成摘要**: `buildSummaryPrompt(snapshot.Messages[sourceTurnStart:])` 而非全部消息
3. **更新 checkpoint**: `SourceTurnStart: sourceTurnStart`（首次为 0，后续为上次 `SourceTurnEnd`）

```go
sourceTurnStart := 0
if snapshot.Metadata != nil {
    if v, ok := snapshot.Metadata["last_compact_turn"]; ok {
        sourceTurnStart = toInt(v)
    }
}
if sourceTurnStart > len(snapshot.Messages) {
    sourceTurnStart = 0
}
newMessages := snapshot.Messages[sourceTurnStart:]
summaryPrompt := buildSummaryPrompt(newMessages)
```

---

**1.9 — 结构化事实提取**

**文件**: `internal/engine/compact_processor.go`
**函数**: `extractFacts`

**具体变更**:
1. **识别编号列表**: 正则匹配 `^\d+[.)]\s` 模式，将每个编号项作为独立事实
2. **识别项目符号列表**: 匹配 `^[-*•]\s` 模式
3. **段落分割**: 先按空行分割段落，再在段落内按句子分割
4. **合并短句**: 如果连续短句（< 20 字符）属于同一段落，合并为一个事实
5. **保留原有句子分割作为 fallback**: 非结构化文本仍按句末标点分割

---

**1.10 — Profile Summary 合并**

**文件**: `internal/engine/compact_processor.go`
**函数**: `mergeProfile`

**具体变更**:
1. **合并而非覆盖**: 将 `existing.Summary = summary` 改为 `existing.Summary = mergeSummaries(existing.Summary, summary)`
2. **`mergeSummaries` 函数**: 如果旧摘要非空，拼接为 `oldSummary + "\n\n---\n\n" + newSummary`
3. **摘要长度限制**: 如果合并后超过 4000 字符，截断旧摘要保留最近部分（按 rune 截断）
4. **保留 SourceSessionID 更新**: 仍更新为当前会话 ID

---

**1.11 — 偏好提取精确匹配**

**文件**: `internal/engine/compact_processor.go`
**函数**: `mergeProfile`

**具体变更**:
1. **否定表达排除**: 在匹配前检查是否包含否定词（"don't", "doesn't", "not", "never", "no longer", "dislike"）
2. **词边界匹配**: 使用正则 `\bprefer\b` 和 `\blike\b` 替代 `strings.Contains`，排除 "likewise"、"likely" 等
3. **上下文窗口检查**: 检查 "like"/"prefer" 前 10 个字符内是否有否定词
4. **新增 `isPositivePreference(fact string) bool` 函数**: 封装精确匹配逻辑

```go
func isPositivePreference(fact string) bool {
    lower := strings.ToLower(fact)
    negations := []string{"don't", "doesn't", "not ", "never ", "no longer", "dislike"}
    for _, neg := range negations {
        if strings.Contains(lower, neg) {
            return false
        }
    }
    preferRe := regexp.MustCompile(`\bprefer(s|red|ence)?\b`)
    likeRe := regexp.MustCompile(`\blike(s|d)?\b`)
    return preferRe.MatchString(lower) || likeRe.MatchString(lower)
}
```

---

**1.12 — 原子 Metadata 更新**

**文件**: `internal/engine/compact_processor.go`
**函数**: `executeCompact`

**具体变更**:
1. **不保存整个快照**: 移除 `p.sessions.store.Save(ctx, snapshot)`
2. **重新加载最新会话**: 从存储加载当前最新的 session
3. **仅更新 metadata 和消息**: 在最新会话上更新 `last_compact_at`、`last_compact_turn`、`last_compact_tokens`，以及替换压缩后的消息列表
4. **保留新增消息**: 压缩期间新增的消息（index > snapshot 时的消息数）追加到压缩后的消息列表末尾
5. **保存更新后的最新会话**: `p.sessions.store.Save(ctx, liveSession)`

```go
// 重新加载最新会话
liveSession, err := p.sessions.store.Load(ctx, rc.TenantID, rc.UserID, sessionID)
if err != nil { return err }

// 计算压缩期间新增的消息
snapshotLen := len(snapshot.Messages)
var newMsgsDuringCompact []types.Message
if len(liveSession.Messages) > snapshotLen {
    newMsgsDuringCompact = liveSession.Messages[snapshotLen:]
}

// 构建压缩后消息列表 = 摘要 + 最近N条 + 压缩期间新增
compactedMessages := buildCompactedMessages(summaryContent, snapshot.Messages, recentRawTurnCount)
compactedMessages = append(compactedMessages, newMsgsDuringCompact...)

liveSession.Messages = compactedMessages
liveSession.Metadata["last_compact_at"] = checkpoint.CommittedAt.Format(time.RFC3339)
// ... 其他 metadata 更新
p.sessions.store.Save(ctx, liveSession)
```

---

**1.13 — CJK Token 估算**

**文件**: `internal/engine/retrieval.go`
**函数**: `estimateTokens`

**具体变更**:
1. **Unicode 感知估算**: 遍历 rune 而非字节，对 ASCII 字符按 4 字符/Token，对 CJK 字符按 1.5 字符/Token
2. **CJK 范围检测**: 使用 `unicode.Is(unicode.Han, r)` 或检查 Unicode 范围 `\u4E00-\u9FFF`、`\u3040-\u309F`（平假名）、`\u30A0-\u30FF`（片假名）、`\uAC00-\uD7AF`（韩文）
3. **保持英文兼容**: 纯 ASCII 文本结果与 `len(s)/4` 一致

```go
func estimateTokens(s string) int {
    if len(s) == 0 { return 0 }
    asciiChars := 0
    cjkChars := 0
    for _, r := range s {
        if r <= 127 {
            asciiChars++
        } else if isCJK(r) {
            cjkChars++
        } else {
            asciiChars++ // 其他 Unicode 按 ASCII 处理
        }
    }
    tokens := asciiChars/4 + (cjkChars*2+1)/3  // CJK: ~1.5 char/token → 2/3 token per char
    if tokens == 0 && len(s) > 0 { tokens = 1 }
    return tokens
}
```

---

**1.14 — 批量嵌入调用**

**文件**: `internal/engine/context_builder.go`
**函数**: `matchAndLoadSkills`

**具体变更**:
1. **收集所有技能文本**: 构建 `[]string` 包含所有 `skill.Name + " " + skill.Description`
2. **单次批量调用**: `skillVecs, err := b.embedding.Embed(ctx, allSkillTexts)`
3. **按索引映射结果**: `skillVecs[i]` 对应 `catalog[i]` 的嵌入向量
4. **移除循环内的 Embed 调用**: 删除 `for _, skill := range catalog { ... b.embedding.Embed(...) }` 中的逐个调用

---

**1.15 — 动态记忆预算**

**文件**: `internal/engine/context_builder.go`
**函数**: `Assemble`

**具体变更**:
1. **动态计算记忆预算**: 基于会话历史长度和 profile 大小动态分配
2. **公式**: `memBudget = budget * memRatio`，其中 `memRatio` 根据历史消息数量调整
   - 历史消息 ≤ 3 条: `memRatio = 0.4`（更多预算给记忆）
   - 历史消息 4-10 条: `memRatio = 0.25`（平衡分配）
   - 历史消息 > 10 条: `memRatio = 0.15`（更多预算给历史）
3. **最小/最大限制**: `memBudget` 不低于 `budget * 0.1`，不高于 `budget * 0.5`

---

**1.16 — UTF-8 安全截断**

**文件**: `internal/engine/retrieval.go` + `internal/engine/context_builder.go`
**函数**: `applyContentLevel`, `demoteBlock`

**具体变更**:
1. **新增 `truncateRunes(s string, maxRunes int) string` 工具函数**: 按 rune 截断
2. **替换 `content[:200]`**: 改为 `truncateRunes(content, 200)`
3. **替换 `content[:1000]`**: 改为 `truncateRunes(content, 1000)`
4. **SemanticSearch 和 PatternSearch 中的截断**: `content[:maxChars]` 也改为 rune 安全截断

```go
func truncateRunes(s string, maxRunes int) string {
    runes := []rune(s)
    if len(runes) <= maxRunes {
        return s
    }
    return string(runes[:maxRunes])
}
```

---

**1.17 — 历史消息角色保留**

**文件**: `internal/engine/context_builder.go`
**函数**: `Assemble` (构建历史块) + `buildMessages`

**具体变更**:
1. **在 ContentBlock 中保存原始角色**: 利用 `Metadata` 字段或在 `ContentBlock` 中新增 `Role` 字段（推荐在 `types.ContentBlock` 中添加 `OriginalRole string`）
2. **构建历史块时保存角色**: `blocks = append(blocks, types.ContentBlock{..., OriginalRole: msg.Role})`（如果不加字段，则在 Content 中保留 `[role]:` 前缀并在 buildMessages 中解析）
3. **`buildMessages` 解析角色**: 从 Content 的 `[role]: content` 格式中提取 role，或直接使用 `OriginalRole` 字段

**推荐方案（无需修改 types）**: 在 `buildMessages` 中解析已有的 `[role]: content` 格式：
```go
func buildMessages(blocks []types.ContentBlock) []types.Message {
    var msgs []types.Message
    for _, blk := range blocks {
        if blk.Source != "history" { continue }
        role, content := parseRoleContent(blk.Content)
        msgs = append(msgs, types.Message{Role: role, Content: content})
    }
    return msgs
}

func parseRoleContent(s string) (string, string) {
    if strings.HasPrefix(s, "[") {
        if idx := strings.Index(s, "]: "); idx > 0 {
            role := s[1:idx]
            if role == "user" || role == "assistant" || role == "system" {
                return role, s[idx+3:]
            }
        }
    }
    return "user", s
}
```

---

**1.18 — 全级别渐进式截断**

**文件**: `internal/engine/context_builder.go`
**函数**: `Assemble` (progressive loading 循环)

**具体变更**:
1. **移除 `blk.Level == types.ContentL2` 条件**: 对所有级别的块尝试截断
2. **L0/L1 块截断策略**: 如果块超出预算，按剩余预算截断内容（使用 rune 安全截断）
3. **最小有用长度**: 如果剩余预算 < 50 Token，跳过该块（避免过度碎片化）

```go
for _, blk := range blocks {
    if usedTokens+blk.Tokens > budget {
        remaining := budget - usedTokens
        if remaining < 50 { continue } // 最小有用长度

        // 尝试截断以适应剩余预算
        if blk.Level == types.ContentL2 {
            // 先尝试降级到 L1，再到 L0
            demoted := demoteBlock(blk, types.ContentL1)
            if usedTokens+demoted.Tokens <= budget {
                finalBlocks = append(finalBlocks, demoted)
                usedTokens += demoted.Tokens
                continue
            }
            demoted = demoteBlock(blk, types.ContentL0)
            if usedTokens+demoted.Tokens <= budget {
                finalBlocks = append(finalBlocks, demoted)
                usedTokens += demoted.Tokens
                continue
            }
        }
        // 对所有级别：直接按剩余预算截断
        truncated := truncateBlockToBudget(blk, remaining)
        if truncated.Tokens > 0 {
            finalBlocks = append(finalBlocks, truncated)
            usedTokens += truncated.Tokens
        }
        continue
    }
    finalBlocks = append(finalBlocks, blk)
    usedTokens += blk.Tokens
}
```

## 测试策略

### 验证方法

测试策略遵循两阶段方法：首先在未修复代码上发现反例（Exploratory），确认根因；然后验证修复的正确性（Fix Checking）和保持性（Preservation Checking）。

### 探索性 Bug Condition 检查

**目标**: 在实施修复前，发现能证明缺陷存在的反例，确认或否定根因分析。如果否定，需重新假设根因。

**测试计划**: 编写针对每个缺陷触发条件的测试，在未修复代码上运行以观察失败模式。

**测试用例**:
1. **超时缺失测试 (1.1)**: 使用 mock LLM 模拟 hang（永不返回），验证 goroutine 是否无限阻塞（未修复代码将阻塞）
2. **竞态条件测试 (1.2)**: 并发调用 `AddMessage` 100 次，使用 `-race` 检测数据竞争（未修复代码将报告 race）
3. **Panic Recovery 测试 (1.3)**: mock `tasks.Start` 触发 panic，验证 semaphore 是否泄漏（未修复代码 semaphore 永久占用）
4. **DLQ 累积测试 (1.4)**: mock store 持续失败，验证 DLQ 是否无限增长（未修复代码 DLQ 满后静默丢弃）
5. **Shutdown 中断测试 (1.5)**: 在 compact 执行中调用 Shutdown，验证 checkpoint 是否持久化（未修复代码可能中断）
6. **负数 TokenBudget 测试 (1.6)**: 发送 `TokenBudget: -1` 的请求，验证是否返回 400（未修复代码将接受）
7. **压缩无效测试 (1.7)**: 执行压缩后检查 `session.Messages` 长度，验证是否减少（未修复代码长度不变）
8. **SourceTurnStart 测试 (1.8)**: 连续两次压缩，验证第二次的 `SourceTurnStart` 是否 > 0（未修复代码始终为 0）
9. **结构化文本提取测试 (1.9)**: 输入 "1. Fact A\n2. Fact B"，验证提取结果（未修复代码可能碎片化）
10. **Summary 覆盖测试 (1.10)**: 两次压缩后检查 profile.Summary 是否包含两次的内容（未修复代码仅含最后一次）
11. **否定偏好测试 (1.11)**: 输入 "I don't like spicy food"，验证是否被存为偏好（未修复代码会错误存储）
12. **快照覆盖测试 (1.12)**: 压缩期间添加新消息，验证新消息是否丢失（未修复代码会丢失）
13. **CJK Token 测试 (1.13)**: 输入 "你好世界测试"，比较估算值与实际 Token 数（未修复代码严重低估）
14. **N+1 调用测试 (1.14)**: mock embedding 计数调用次数，50 个技能验证调用次数（未修复代码 50 次）
15. **硬编码预算测试 (1.15)**: 短会话 + 丰富记忆场景，验证记忆预算分配（未修复代码固定 25%）
16. **UTF-8 截断测试 (1.16)**: 输入 200 个中文字符，截断后验证 UTF-8 有效性（未修复代码可能无效）
17. **角色丢失测试 (1.17)**: 包含 assistant 消息的历史，验证 buildMessages 输出角色（未修复代码全为 "user"）
18. **L0 跳过测试 (1.18)**: 大型 L0 记忆块超预算，验证是否尝试截断（未修复代码直接跳过）

**预期反例**:
- 1.2: `go test -race` 报告 `DATA RACE on session.Messages`
- 1.7: 压缩后 `len(session.Messages)` 不变
- 1.13: "你好世界" 估算为 3 Token，实际应 ≥ 4
- 1.17: assistant 消息的 Role 变为 "user"

### Fix Checking

**目标**: 验证对所有满足 bug condition 的输入，修复后的函数产生期望行为。

**伪代码:**
```
FOR ALL input WHERE isBugCondition(input) DO
  result := fixedFunction(input)
  ASSERT expectedBehavior(result)
END FOR
```

### Preservation Checking

**目标**: 验证对所有不满足 bug condition 的输入，修复后的函数产生与原始函数相同的结果。

**伪代码:**
```
FOR ALL input WHERE NOT isBugCondition(input) DO
  ASSERT originalFunction(input) = fixedFunction(input)
END FOR
```

**测试方法**: 推荐使用属性基测试（Property-Based Testing），因为：
- 自动生成大量测试用例覆盖输入域
- 捕获手动单元测试可能遗漏的边界情况
- 对非缺陷输入的行为不变性提供强保证

**测试计划**: 先在未修复代码上观察非缺陷输入的行为，然后编写属性基测试捕获该行为。

**测试用例**:
1. **shouldTrigger 保持性**: 生成随机 session metadata，验证修复前后 `shouldTrigger` 结果一致
2. **纯英文 Token 估算保持性**: 生成随机 ASCII 字符串，验证修复后 `estimateTokens` 与 `len(s)/4` 误差 ≤ 10%
3. **三层缓存保持性**: 验证 `AddMessage` 修复后仍正确写入 LRU、Redis、PG 队列
4. **合法 API 请求保持性**: 生成合法参数范围内的请求，验证修复后正常处理
5. **空技能目录保持性**: 技能目录为空时，验证不调用 embedding API
6. **渐进式加载保持性**: 所有块在预算内时，验证按分数降序完整包含

### 单元测试

- `TestEstimateTokens_CJK`: 验证中文、日文、韩文文本的 Token 估算准确性
- `TestEstimateTokens_ASCII_Preservation`: 验证纯英文文本估算与 `len(s)/4` 一致
- `TestTruncateRunes_ValidUTF8`: 验证截断后字符串始终是有效 UTF-8
- `TestExtractFacts_StructuredText`: 验证编号列表和项目符号的正确解析
- `TestIsPositivePreference`: 验证否定表达和无关子串的排除
- `TestMergeSummaries`: 验证新旧摘要合并而非覆盖
- `TestBuildMessages_RolePreservation`: 验证历史消息角色正确解析
- `TestParseRoleContent`: 验证 `[role]: content` 格式的正确解析
- `TestValidateAssembleRequest`: 验证非法参数返回错误
- `TestCompactReducesMessages`: 验证压缩后消息数量减少
- `TestSourceTurnStartIncremental`: 验证增量压缩的 SourceTurnStart 正确设置
- `TestAtomicMetadataUpdate`: 验证压缩期间新增消息不丢失
- `TestDLQConsumer`: 验证 DLQ 重试机制
- `TestPanicRecovery`: 验证 panic 后资源正确释放
- `TestContextTimeout`: 验证外部调用超时后正确取消
- `TestDynamicMemoryBudget`: 验证不同会话长度下的记忆预算分配
- `TestProgressiveLoadAllLevels`: 验证 L0/L1 块超预算时的截断行为
- `TestBatchEmbedSkills`: 验证技能嵌入为单次批量调用

### 属性基测试

- 生成随机 Unicode 字符串（含 CJK），验证 `estimateTokens` 结果 ≥ `len([]rune(s)) / 4` 且 ≤ `len([]rune(s))`
- 生成随机 session metadata，验证 `shouldTrigger` 修复前后结果一致
- 生成随机内容字符串，验证 `truncateRunes` 输出始终是有效 UTF-8 且 rune 数 ≤ maxRunes
- 生成随机事实文本（含否定词），验证 `isPositivePreference` 不将否定表达归为偏好
- 生成随机 `[role]: content` 格式字符串，验证 `parseRoleContent` 正确提取角色
- 生成随机合法/非法 API 参数，验证校验函数正确区分

### 集成测试

- 端到端压缩流程：Ingest → 触发压缩 → 验证消息减少 + checkpoint 持久化 + hooks 触发
- 并发 AddMessage + Compact：多 goroutine 同时写入和压缩，验证无数据竞争和消息丢失
- Shutdown 期间 Compact：启动压缩后立即 Shutdown，验证 checkpoint 完整持久化
- 多会话 Profile 合并：两个不同会话分别压缩，验证 profile summary 包含两次内容
- CJK 全链路：中文消息 → 压缩 → Token 估算 → 截断 → 验证 UTF-8 有效性
