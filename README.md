# CPA Intent Router

基于 CLIProxyAPI 动态插件体系实现的上下文感知与小模型意图分类两阶段动态模型路由插件。

---

## 路由架构与规则分流示意

```text
┌────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│  [按意图]   [按规则]   [超长上下文]   [多模态]   [思考深度]   智能决策：首轮解析分流规则链与意图，同轮工具调用严格锁定         │
├────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┤
│                                                                                                                                │
│   ┌─────────────────┐             ┌───────────────────────┐         【rules 分流规则链（从上到下顺序评估，首条命中即终止）】   │
│   │   Claude Code   │             │      CLIProxyAPI      │                                                                │
│   │      Codex      │ ──────────► │   cpa-intent-router   │ ───────► 规则 1: tokens ≥ 200k ───────► gemini-2.5-pro (超长文本) │
│   │    OpenCode     │             │                       │ ───────► 规则 2: images = true ───────► qwen-vl-max (视觉模态)   │
│   │   你的 Agent    │             │   虚拟组: group/dev   │ ───────► 规则 3: effort = high ───────► claude-3-7-sonnet:high │
│   └─────────────────┘             │                       │ ───────► 规则 4: compact = true ──────► deepseek-flash (会话压缩)│
│                                   │   [宿主内部原生 RPC]  │ ───────► 规则 5: intent="quick question"                       │
│                                   │   host.model.execute  │              │ (宿主内部 RPC 极简调用 classifier 小模型)       │
│                                   │           │           │              └────────────────────────► deepseek-chat (快速问答) │
│                                   │           ▼           │                                                                │
│                                   │    gemini-2.5-flash   │ ───────► [全部未命中兜底 fallback] ───► claude-3-7-sonnet (主力)   │
│                                   └───────────────────────┘                                                                    │
│                                                                                                                                │
│   客户端向虚拟模型 group/dev 发起请求，插件按顺序匹配 rules 规则分流；同轮多步工具交互自动锁定同一模型，防止会话漂移。         │
└────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

```mermaid
flowchart TD
    subgraph Client [客户端 / Agent]
        A[Claude Code / Codex / 你的 Agent]
    end

    subgraph Gateway [CLIProxyAPI + 插件 (ModelRouter ABI)]
        B[虚拟路由组 group/dev]
        S{同轮工具交互<br/>Tool Result ?}
        Lock[锁定首轮模型<br/>保持会话亲和]
    end

    subgraph RuleChain [rules 分流规则链（从上到下顺序匹配，首个命中即终止）]
        R1{规则 1: tokens ≥ 200k ?}
        R2{规则 2: images = true ?}
        R3{规则 3: effort = high ?}
        R4{规则 4: compact 压缩 ?}
        R5{规则 5: intent 意图匹配 ?}
        Fallback[兜底模型 fallback]
    end

    subgraph ClassifierEngine [小模型意图分类（宿主内部 RPC）]
        RPC[宿主原生 host.model.execute<br/>零网络开销 / 进程内直接执行]
        C[Classifier 小模型<br/>gemini-2.5-flash]
        Cache[(10分钟 SHA-256 缓存<br/>+ Singleflight 防重)]
    end

    subgraph Targets [目标模型池]
        M1[gemini-2.5-pro<br/>超长上下文]
        M2[qwen-vl-max<br/>多模态模型]
        M3[claude-3-7-sonnet:high<br/>深度推理思考]
        M4[deepseek-flash<br/>低成本会话压缩]
        M5[deepseek-chat<br/>轻量快速问答]
        M_FB[claude-3-7-sonnet<br/>默认主力模型]
    end

    A -->|请求 group/dev| B
    B --> S
    S -- 是 (同一Turn交互) --> Lock
    S -- 否 (新对话轮次) --> R1

    R1 -- 命中 --> M1
    R1 -- 未命中 --> R2
    R2 -- 命中 --> M2
    R2 -- 未命中 --> R3
    R3 -- 命中 --> M3
    R3 -- 未命中 --> R4
    R4 -- 命中 --> M4
    R4 -- 未命中 --> R5

    R5 -- 需要意图判别 --> RPC
    RPC --> Cache --> C
    C -- 命中 quick question --> M5

    R5 -- 未命中/无意图 --> Fallback --> M_FB
```

---

## 核心能力

1. **虚拟路由组（Routing Group）**：支持将异构模型编组（如 `group/dev` 或 `dev`），并对外暴露统一虚拟模型入口，自动注册至 `/v1/models`。
2. **两阶段动态规则分流（rules 规则链）**：
   - **第一阶段（零额外开销静态规则）**：按 Token 估算阈值、多模态图片输入、推理思考深度要求（`effort`）、客户端标识（`agents`）、会话压缩标志（`compact`）及时间窗口进行前置匹配。
   - **第二阶段（宿主内部小模型极简意图分类）**：支持自然语言描述意图（如 `"a quick question"`, `"writing or fixing tests"`），通过宿主进程内原生 RPC（`host.model.execute`）直接调用轻量模型极简分类（内置 10 分钟 SHA-256 缓存与 Singleflight 防重，**无需配置任何外部 HTTP 地址或 API Key**）。
3. **轮次亲和性锁定（Turn Affinity Locking）**：精准识别多步工具交互轮次（Tool Call / Tool Result），同一轮次交互严格锁定在首轮选定模型，防止模型漂移。

---

## 构建方式

```bash
# 构建 Linux amd64 动态库插件
CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -trimpath -buildmode=c-shared -o cpa-intent-router.so .
```

将生成的 `cpa-intent-router.so` 放置于 CLIProxyAPI 的插件目录（如 `/data/cli/plugins/linux/amd64/`）即可随宿主自动加载。

---

## 详细配置说明

在 CLIProxyAPI 的 `config.yaml` 中配置插件段（或使用 `config_file` 指向独立文件）：

```yaml
plugins:
  configs:
    cpa-intent-router:
      enabled: true                     # 是否启用意图路由插件（默认 true）

      # 虚拟路由组定义
      groups:
        - id: "opus-anywhere"           # 组唯一标识，客户端请求写为 "group/opus-anywhere" 或 "opus-anywhere"
          name: "Opus Anywhere 智能组"   # 组可读名称
          members:                      # 候选模型池
            - "openrouter/google/gemini-2.5-pro"
            - "deepseek-chat"
            - "claude/claude-3-7-sonnet"
          classifier: "gemini/gemini-2.5-flash" # 前置意图分类小模型（宿主内部 RPC 直接调用）
          fallback: "claude/claude-3-7-sonnet"  # 兜底模型：所有规则未命中时生效

          # rules: 分流匹配规则链（从上到下顺序匹配，首个完全命中即生效）
          rules:
            # 1. 超长上下文分流 (Tokens >= 200,000)
            - use: "openrouter/google/gemini-2.5-pro"
              tokens: 200000

            # 2. 多模态视觉分流 (携带图片/附件)
            - use: "deepseek-chat"
              images: true

            # 3. 意图分类分流 (由内部 classifier 小模型判定，享 10 分钟缓存)
            - use: "deepseek-chat"
              intent: "a quick question"

            # 4. 深度推理思考分流 (客户端要求 reasoning_effort=high 时)
            - use: "claude/claude-3-7-sonnet:high"
              effort: "high"

            # 5. 会话压缩分流 (客户端触发 /compact 自动摘要时)
            - use: "deepseek-chat"
              compact: true

            # 6. 时间窗口分流 (特定时段生效，支持跨午夜)
            - use: "deepseek-chat"
              time:
                from: "14:00"
                to: "18:00"
                days: ["mon", "tue", "wed", "thu", "fri"]

            # 7. 客户端来源分流 (根据 User-Agent 识别客户端)
            - use: "claude/claude-3-7-sonnet"
              agents: ["claude", "codex"]
```
