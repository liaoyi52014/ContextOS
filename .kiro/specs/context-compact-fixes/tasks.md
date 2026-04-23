# 实现计划

## 缺陷修复任务列表

涵盖 18 个缺陷：A 类（基础设施与可靠性 1.1-1.6）和 B 类（上下文管理与压缩逻辑 1.7-1.18）。

---

- [x] 1. 编写 Bug Condition 探索性测试（修复前）
  - **Property 1: Bug Condition** - 上下文压缩系统 18 项缺陷验证
  - **重要**: 此属性基测试必须在实施修复前编写
  - **目标**: 发现反例证明缺陷存在，确认根因分析
  - **预期结果**: 测试在未修复代码上 FAIL（证明缺陷存在）
  - **不要尝试修复测试或代码**
  - 测试文件: `internal/engine/compact_processor_bug_condition_test.go` 和 `internal/engine/context_builder_bug_condition_test.go`
  - **A 类缺陷探索测试**:
    - 1.1 超时缺失: 使用 mock LLM 模拟永不返回，验证 `executeCompact` 中 `p.llm.Complete(ctx, ...)` 使用的 ctx 无 deadline（`ctx.Deadline()` 返回 false）
    - 1.2 竞态条件: 并发调用 `AddMessage` 100 次，使用 `-race` 检测 `session.Messages` 数据竞争；验证 `putRedis` 和 `syncQueue.Enqueue` 使用的是共享引用而非深拷贝
    - 1.3 Panic Recovery: mock `tasks.Start` 触发 panic，验证 semaphore slot 是否泄漏（`len(p.semaphore)` 不归零）
    - 1.4 DLQ 无消费: mock store 持续失败，验证 DLQ 只增不减，无消费 goroutine
    - 1.5 Shutdown 不等待: 在 compact goroutine 执行中调用 `Shutdown`，验证 compact 被中断
    - 1.6 输入无校验: 发送 `TokenBudget: -1`、`TopK: -1`、`Messages` 超大数组，验证未返回 400
  - **B 类缺陷探索测试**:
    - 1.7 压缩无效: 执行压缩后检查 `session.Messages` 长度不变（未替换为摘要+最近N条）
    - 1.8 SourceTurnStart=0: 连续两次压缩，验证第二次 `checkpoint.SourceTurnStart` 仍为 0
    - 1.9 事实提取粗糙: 输入 `"1. Fact A\n2. Fact B\n- Item C"` 验证 `extractFacts` 产生碎片化结果
    - 1.10 Summary 覆盖: 两次压缩后检查 `profile.Summary` 仅包含最后一次内容
    - 1.11 偏好误判: 输入 `"I don't like spicy food"` 和 `"likewise"` 验证被错误存为偏好
    - 1.12 快照覆盖: 压缩期间添加新消息，验证 `p.sessions.store.Save(ctx, snapshot)` 覆盖了新消息
    - 1.13 CJK Token 低估: `estimateTokens("你好世界测试")` 返回 `15/4=3`，实际应 ≥ 5
    - 1.14 N+1 嵌入调用: mock embedding 计数调用次数，50 个技能产生 50 次调用
    - 1.15 硬编码预算: 验证 `memBudget` 始终为 `budget/4`，不随会话长度变化
    - 1.16 UTF-8 截断: 输入 200+ 个中文字符，`demoteBlock` 按字节截断产生无效 UTF-8
    - 1.17 角色丢失: 包含 `[assistant]: ...` 的历史块，`buildMessages` 输出 `Role` 全为 `"user"`
    - 1.18 L0 跳过: 大型 L0 记忆块超预算时直接跳过，未尝试截断
  - 运行测试，记录失败反例
  - 任务完成标准: 测试编写完成、运行完成、失败已记录
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 1.6, 1.7, 1.8, 1.9, 1.10, 1.11, 1.12, 1.13, 1.14, 1.15, 1.16, 1.17, 1.18_

- [x] 2. 编写保持性属性测试（修复前）
  - **Property 2: Preservation** - 非缺陷输入行为保持不变
  - **重要**: 遵循观察优先方法论，在未修复代码上观察行为后编写测试
  - **预期结果**: 测试在未修复代码上 PASS（确认基线行为）
  - 测试文件: `internal/engine/preservation_property_test.go`
  - **保持性测试项**:
    - 3.1 shouldTrigger 保持性: 生成随机 session metadata（消息数未达触发条件），验证 `shouldTrigger` 返回 false，四个触发条件逻辑不变
    - 3.2 压缩完成流程保持性: 验证压缩正常完成时生成 `CompactCheckpoint`、触发 hooks/webhooks、记录 Token 审计
    - 3.3 向量存储不可用保持性: 向量存储或嵌入服务不可用时，压缩其余步骤（摘要、画像合并）继续执行
    - 3.4 纯英文 Token 估算保持性: 生成随机 ASCII 字符串，验证 `estimateTokens` 结果与 `len(s)/4` 误差 ≤ 10%
    - 3.5 空技能目录保持性: 技能目录为空时跳过匹配，不调用嵌入 API
    - 3.6 新用户画像保持性: 用户画像不存在时创建新空画像并填充当前会话信息
    - 3.7 三层缓存保持性: `AddMessage` 正确执行 LRU → Redis → PG 异步队列写入和 `MaxMessages` 裁剪
    - 3.8 渐进式加载保持性: 所有块在预算内时按分数降序完整包含，不进行不必要截断
    - 3.9 分布式锁保持性: 锁已被持有时跳过压缩（非阻塞）
    - 3.10 语义搜索保持性: 按相似度降序排列并在预算内截断
    - 3.11 SyncQueue 停止保持性: 队列停止时 `Enqueue` 返回错误
    - 3.12 合法请求保持性: 合法 API 请求（参数在有效范围内）正常处理
  - 在未修复代码上运行测试，验证全部 PASS
  - 任务完成标准: 测试编写完成、运行完成、全部通过
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 3.9, 3.10, 3.11, 3.12_

- [x] 3. A 类修复：基础设施与可靠性（1.1-1.6）

  - [x] 3.1 修复 1.1 — Context Timeout 缺失
    - 在 `CompactConfig` 中添加 `LLMTimeoutSec`、`EmbedTimeoutSec`、`VectorTimeoutSec` 字段（默认 60、30、30 秒）
    - `executeCompact` 中 LLM 调用前使用 `context.WithTimeout(ctx, llmTimeout)` 包装
    - Embedding 调用前使用 `context.WithTimeout(ctx, embedTimeout)` 包装
    - VectorStore 调用前使用 `context.WithTimeout(ctx, vectorTimeout)` 包装
    - goroutine 入口处将 `compactCtx := context.Background()` 改为 `context.WithTimeout(context.Background(), overallTimeout)` 并 `defer cancel()`
    - _Bug_Condition: input.type == "external_call" AND input.context HAS NO timeout_
    - _Expected_Behavior: 超时后自动取消请求并返回错误_
    - _Preservation: shouldTrigger 逻辑不变，压缩完成流程不变_
    - _Requirements: 1.1, 2.1_

  - [x] 3.2 修复 1.2 — SessionManager 竞态条件
    - `AddMessage` 在 `m.mu.Lock()` 块内修改 `session.Messages` 后立即调用 `cloneSession(session)` 生成深拷贝
    - `m.mu.Unlock()` 后使用拷贝调用 `putLRU`、`putRedis`、`syncQueue.Enqueue`
    - 确保锁外操作不使用原始 session 指针
    - _Bug_Condition: input.type == "add_message" AND input.session IS shared_reference AND concurrent_access_
    - _Expected_Behavior: 锁内深拷贝，锁外使用拷贝，消除数据竞争_
    - _Preservation: 三层缓存写入顺序和 MaxMessages 裁剪逻辑不变_
    - _Requirements: 1.2, 2.2, 3.7_

  - [x] 3.3 修复 1.3 — Compact Goroutine Panic Recovery
    - `EvaluateAndTrigger` 中 `go func()` 入口添加 `defer func() { if r := recover(); r != nil { ... } }()`
    - recover 中释放 `activeLocks.Delete(session.ID)` 和 `<-p.semaphore`
    - 记录 panic 日志并调用 `p.tasks.Fail()` 标记任务失败
    - _Bug_Condition: input.type == "compact_goroutine" AND input.execution CAUSES panic_
    - _Expected_Behavior: panic 被捕获，资源正确释放，错误日志记录_
    - _Preservation: 正常执行路径不受影响_
    - _Requirements: 1.3, 2.3_

  - [x] 3.4 修复 1.4 — DLQ 消费机制
    - 在 `SyncItem` 中添加 `RetryCount int` 字段
    - 新增 `dlqWorker()` 方法：定期（30 秒）从 DLQ 取出 item 重试，最多 3 次
    - `flush` 中 DLQ 写入失败时（`default` 分支）记录 `WARN` 日志
    - `Start()` 中启动 `go q.dlqWorker()`
    - `Stop()` 中等待 dlqWorker 退出
    - _Bug_Condition: input.type == "sync_flush_failure" AND dlq.length > 0 AND NO dlq_consumer_exists_
    - _Expected_Behavior: DLQ 定期重试消费，满时告警，超过重试次数后记录并丢弃_
    - _Preservation: SyncQueue.Enqueue 在队列停止时仍返回错误_
    - _Requirements: 1.4, 2.4, 3.11_

  - [x] 3.5 修复 1.5 — Shutdown 等待 Compact
    - `CompactProcessor` 中添加 `sync.WaitGroup` 追踪活跃 compact goroutine
    - goroutine 入口 `wg.Add(1)`，出口 `defer wg.Done()`
    - 新增 `WaitForCompletion(timeout time.Duration) error` 方法
    - `GracefulShutdown.RegisterCompactFlush` 注册的 flush 函数调用 `WaitForCompletion()`
    - _Bug_Condition: input.type == "shutdown" AND active_compact_goroutines > 0_
    - _Expected_Behavior: Shutdown 等待所有 compact goroutine 完成（带超时）_
    - _Preservation: 分布式锁非阻塞获取行为不变_
    - _Requirements: 1.5, 2.5, 3.9_

  - [x] 3.6 修复 1.6 — API 输入校验
    - `handleAssemble` 中校验: `TokenBudget < 0` → 400, `TopK <= 0`（如有）→ 400
    - `handleIngest` 中校验: `len(Messages) > 200` → 400
    - `handleMemorySearch` 中校验: `Limit < 0` → 400
    - 提取公共校验函数 `validateAssembleRequest` 和 `validateIngestRequest`
    - _Bug_Condition: input.tokenBudget < 0 OR input.topK <= 0 OR len(input.messages) > MAX_LIMIT_
    - _Expected_Behavior: 非法参数返回 HTTP 400_
    - _Preservation: 合法请求正常处理_
    - _Requirements: 1.6, 2.6, 3.12_

- [x] 4. B 类修复：上下文管理与压缩逻辑（1.7-1.12）

  - [x] 4.1 修复 1.7 — 压缩实际减少消息
    - `executeCompact` 中摘要生成后构建新消息列表: `[摘要消息] + 最近 RecentRawTurnCount 条原始消息`
    - 摘要消息格式: `Message{Role: "system", Content: "[Compact Summary] " + summaryContent}`
    - 在 `CompactConfig` 中添加 `RecentRawTurnCount` 字段（默认 8）
    - 更新 `snapshot.Messages` 为压缩后消息列表
    - 基于新消息列表重新计算 `last_compact_tokens`
    - _Bug_Condition: input.type == "compact_complete" AND original_messages NOT replaced_
    - _Expected_Behavior: 压缩后消息数 = 1(摘要) + min(RecentRawTurnCount, 原始消息数)_
    - _Preservation: shouldTrigger 逻辑不变，checkpoint 持久化不变_
    - _Requirements: 1.7, 2.7, 3.1, 3.2_

  - [x] 4.2 修复 1.8 — SourceTurnStart 增量压缩
    - 从 `snapshot.Metadata["last_compact_turn"]` 读取上次压缩位置作为 `SourceTurnStart`
    - 仅对 `snapshot.Messages[sourceTurnStart:]` 生成摘要
    - 首次压缩时 `sourceTurnStart = 0`，后续为上次 `SourceTurnEnd`
    - 边界保护: 如果 `sourceTurnStart > len(snapshot.Messages)` 则重置为 0
    - _Bug_Condition: input.type == "compact_checkpoint" AND sourceTurnStart == 0 ALWAYS_
    - _Expected_Behavior: 非首次压缩时 SourceTurnStart = last_compact_turn_
    - _Preservation: 首次压缩行为不变_
    - _Requirements: 1.8, 2.8_

  - [x] 4.3 修复 1.9 — 结构化事实提取
    - `extractFacts` 中先按空行分割段落
    - 识别编号列表: 正则 `^\d+[.)]\s` 模式，每个编号项作为独立事实
    - 识别项目符号列表: 匹配 `^[-*•]\s` 模式
    - 合并短句: 连续短句（< 20 字符）属于同一段落时合并
    - 非结构化文本仍按句末标点分割（fallback）
    - _Bug_Condition: input.summary CONTAINS structured_content (lists, numbered_items)_
    - _Expected_Behavior: 每个提取的事实是完整的语义单元_
    - _Preservation: 纯句子文本的提取结果不变_
    - _Requirements: 1.9, 2.9_

  - [x] 4.4 修复 1.10 — Profile Summary 合并
    - `mergeProfile` 中将 `existing.Summary = summary` 改为 `existing.Summary = mergeSummaries(existing.Summary, summary)`
    - 新增 `mergeSummaries(old, new string) string`: 旧摘要非空时拼接 `old + "\n\n---\n\n" + new`
    - 合并后超过 4000 rune 时截断旧摘要保留最近部分（按 rune 截断）
    - _Bug_Condition: input.type == "merge_profile" AND existing.Summary != "" AND action == "overwrite"_
    - _Expected_Behavior: 新旧摘要合并，跨会话信息不丢失_
    - _Preservation: 新用户画像创建逻辑不变_
    - _Requirements: 1.10, 2.10, 3.6_

  - [x] 4.5 修复 1.11 — 偏好提取精确匹配
    - 新增 `isPositivePreference(fact string) bool` 函数
    - 否定表达排除: 检查 "don't", "doesn't", "not ", "never ", "no longer", "dislike"
    - 词边界匹配: 使用正则 `\bprefer(s|red|ence)?\b` 和 `\blike(s|d)?\b` 替代 `strings.Contains`
    - 排除 "likewise"、"likely" 等无关子串
    - `mergeProfile` 中用 `isPositivePreference` 替换原有 `strings.Contains` 逻辑
    - _Bug_Condition: input.fact CONTAINS negation OR false_match_
    - _Expected_Behavior: 否定表达和无关子串不被归为偏好_
    - _Preservation: 真正的肯定偏好仍被正确识别_
    - _Requirements: 1.11, 2.11_

  - [x] 4.6 修复 1.12 — 原子 Metadata 更新（消除快照覆盖）
    - 移除 `p.sessions.store.Save(ctx, snapshot)` 整体保存
    - 压缩完成后重新加载最新会话: `liveSession, err := p.sessions.store.Load(ctx, ...)`
    - 计算压缩期间新增消息: `liveSession.Messages[len(snapshot.Messages):]`
    - 构建压缩后消息列表: `摘要 + 最近N条 + 压缩期间新增`
    - 仅更新 `liveSession` 的 metadata 和消息列表后保存
    - _Bug_Condition: snapshot OVERWRITES live_session WITH stale_data_
    - _Expected_Behavior: 仅原子更新 metadata 和压缩后消息，保留压缩期间新增消息_
    - _Preservation: checkpoint 持久化和 hooks 触发不变_
    - _Requirements: 1.12, 2.12, 3.2_

- [x] 5. B 类修复：上下文管理与压缩逻辑（1.13-1.18）

  - [x] 5.1 修复 1.13 — CJK Token 估算准确性
    - 重写 `estimateTokens` 函数: 遍历 rune 而非字节
    - ASCII 字符按 4 字符/Token，CJK 字符按 ~1.5 字符/Token（即 2/3 Token/字符）
    - CJK 范围检测: `unicode.Is(unicode.Han, r)` 及平假名、片假名、韩文范围
    - 纯 ASCII 文本结果与 `len(s)/4` 保持一致
    - _Bug_Condition: input.text CONTAINS CJK_characters_
    - _Expected_Behavior: CJK 文本估算值不低于实际 Token 数_
    - _Preservation: 纯英文文本估算结果与 len(s)/4 误差 ≤ 10%_
    - _Requirements: 1.13, 2.13, 3.4_

  - [x] 5.2 修复 1.14 — 批量嵌入调用
    - `matchAndLoadSkills` 中收集所有技能文本到 `[]string`
    - 单次调用 `b.embedding.Embed(ctx, allSkillTexts)` 获取所有嵌入向量
    - 按索引映射: `skillVecs[i]` 对应 `catalog[i]`
    - 移除循环内逐个 `Embed` 调用
    - _Bug_Condition: len(catalog) > 1 AND embedding_calls == len(catalog)_
    - _Expected_Behavior: N 个技能仅 1 次 Embed 调用_
    - _Preservation: 空技能目录时不调用嵌入 API_
    - _Requirements: 1.14, 2.14, 3.5_

  - [x] 5.3 修复 1.15 — 动态记忆预算
    - `Assemble` 中根据会话历史长度动态计算 `memRatio`:
      - 历史消息 ≤ 3 条: `memRatio = 0.4`
      - 历史消息 4-10 条: `memRatio = 0.25`
      - 历史消息 > 10 条: `memRatio = 0.15`
    - `memBudget = budget * memRatio`，限制在 `[budget*0.1, budget*0.5]` 范围内
    - _Bug_Condition: memBudget == budget/4 ALWAYS (hardcoded)_
    - _Expected_Behavior: 记忆预算根据会话长度动态调整_
    - _Preservation: 中等长度会话（4-10条）的预算分配与原 25% 近似_
    - _Requirements: 1.15, 2.15_

  - [x] 5.4 修复 1.16 — UTF-8 安全截断
    - 新增 `truncateRunes(s string, maxRunes int) string` 工具函数
    - `demoteBlock` 中 `content[:200]` → `truncateRunes(content, 200)`，`content[:1000]` → `truncateRunes(content, 1000)`
    - `applyContentLevel` 中同样替换为 rune 安全截断
    - `SemanticSearch` 和 `PatternSearch` 中 `content[:maxChars]` 也改为 rune 安全截断
    - _Bug_Condition: input.content CONTAINS multibyte_chars AND truncation_at_byte_boundary_
    - _Expected_Behavior: 截断后字符串始终是有效 UTF-8_
    - _Preservation: 纯 ASCII 文本截断行为不变_
    - _Requirements: 1.16, 2.16_

  - [x] 5.5 修复 1.17 — 历史消息角色保留
    - `buildMessages` 中解析 `[role]: content` 格式提取原始角色
    - 新增 `parseRoleContent(s string) (string, string)` 函数
    - 支持 "user"、"assistant"、"system" 三种角色
    - 无法解析时默认为 "user"（向后兼容）
    - _Bug_Condition: history_block.original_role != "user" AND output.role == "user"_
    - _Expected_Behavior: 保留原始消息角色（user/assistant/system）_
    - _Preservation: 原本就是 user 角色的消息不受影响_
    - _Requirements: 1.17, 2.17_

  - [x] 5.6 修复 1.18 — 全级别渐进式截断
    - `Assemble` 渐进式加载循环中移除 `blk.Level == types.ContentL2` 条件限制
    - 对所有级别块: 先尝试 L2→L1→L0 降级（仅 L2），再尝试按剩余预算直接截断
    - 新增 `truncateBlockToBudget(blk ContentBlock, remainingTokens int) ContentBlock` 函数
    - 最小有用长度: 剩余预算 < 50 Token 时跳过（避免碎片化）
    - _Bug_Condition: block.level IN [L0, L1] AND block.tokens > remaining_budget AND NO truncation_attempted_
    - _Expected_Behavior: L0/L1 块超预算时尝试截断适应剩余预算_
    - _Preservation: 所有块在预算内时按分数降序完整包含_
    - _Requirements: 1.18, 2.18, 3.8_

- [x] 6. 验证修复 — Bug Condition 探索测试通过

  - [x] 6.1 验证 Bug Condition 探索测试现在通过
    - **Property 1: Expected Behavior** - 18 项缺陷修复后期望行为验证
    - **重要**: 重新运行任务 1 中的同一测试，不要编写新测试
    - 任务 1 中的测试编码了期望行为，通过即确认修复正确
    - 运行 `internal/engine/compact_processor_bug_condition_test.go` 和 `internal/engine/context_builder_bug_condition_test.go`
    - **预期结果**: 测试 PASS（确认缺陷已修复）
    - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6, 2.7, 2.8, 2.9, 2.10, 2.11, 2.12, 2.13, 2.14, 2.15, 2.16, 2.17, 2.18_

  - [x] 6.2 验证保持性测试仍然通过
    - **Property 2: Preservation** - 非缺陷输入行为保持不变
    - **重要**: 重新运行任务 2 中的同一测试，不要编写新测试
    - 运行 `internal/engine/preservation_property_test.go`
    - **预期结果**: 测试 PASS（确认无回归）
    - 确认所有保持性测试在修复后仍通过
    - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 3.6, 3.7, 3.8, 3.9, 3.10, 3.11, 3.12_

- [x] 7. 单元测试补充

  - [x] 7.1 编写 A 类修复单元测试
    - `TestContextTimeout`: 验证外部调用超时后正确取消
    - `TestAddMessage_RaceCondition`: 并发 `AddMessage` 使用 `-race` 无数据竞争
    - `TestPanicRecovery`: panic 后 semaphore 和 activeLock 正确释放
    - `TestDLQConsumer`: DLQ 重试机制正确消费失败 item
    - `TestShutdownWaitsCompact`: Shutdown 等待 compact 完成
    - `TestValidateAssembleRequest`: 非法参数返回错误
    - `TestValidateIngestRequest`: Messages 超限返回错误
    - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 2.6_

  - [x] 7.2 编写 B 类修复单元测试
    - `TestCompactReducesMessages`: 压缩后消息数量减少
    - `TestSourceTurnStartIncremental`: 增量压缩 SourceTurnStart 正确设置
    - `TestExtractFacts_StructuredText`: 编号列表和项目符号正确解析
    - `TestMergeSummaries`: 新旧摘要合并而非覆盖
    - `TestIsPositivePreference`: 否定表达和无关子串排除
    - `TestAtomicMetadataUpdate`: 压缩期间新增消息不丢失
    - `TestEstimateTokens_CJK`: 中文、日文、韩文 Token 估算准确
    - `TestEstimateTokens_ASCII_Preservation`: 纯英文与 `len(s)/4` 一致
    - `TestBatchEmbedSkills`: 技能嵌入为单次批量调用
    - `TestDynamicMemoryBudget`: 不同会话长度下记忆预算分配
    - `TestTruncateRunes_ValidUTF8`: 截断后始终有效 UTF-8
    - `TestBuildMessages_RolePreservation`: 历史消息角色正确解析
    - `TestParseRoleContent`: `[role]: content` 格式正确解析
    - `TestProgressiveLoadAllLevels`: L0/L1 块超预算时截断行为
    - _Requirements: 2.7, 2.8, 2.9, 2.10, 2.11, 2.12, 2.13, 2.14, 2.15, 2.16, 2.17, 2.18_

- [x] 8. Checkpoint — 确保所有测试通过
  - 运行完整测试套件: `go test ./internal/engine/... ./internal/api/... ./internal/cluster/... -race -v`
  - 确认所有 Bug Condition 探索测试通过（Property 1）
  - 确认所有保持性属性测试通过（Property 2）
  - 确认所有单元测试通过
  - 如有问题，与用户沟通解决
  - _Requirements: 2.1-2.18, 3.1-3.12_
