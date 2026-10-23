# Business 高级生成参数：公开证据与探测边界

本次仅核对公开 Google 文档、无需登录的 widget JavaScript、固定 SDK 源码和已有脱敏拒绝记录。没有读取账号、调用模型或启动被系统阻断的测试包。本文是实施依据，不能代替目标账号的运行验收。

## 新发现：thinkingLevel 有独立容器，但值来自目标 agent 元数据

公开 [widget loader](https://cloud.google.com/ai/gen-app-builder/client) 的 `axVgKc` 指向 [固定版本的 Google widget bundle](https://www.gstatic.com/_/mss/boq-cloud-ai-ucs/_/js/k=boq-cloud-ai-ucs.UcsWidgetUi.en_US.pveFsgaw8rQ.2021.O/am=AACAERg/d=1/rs=AAj4Nmy6tdJplhe-n3Ngu_SbpIesx-rN-Q/m=bootstrap_module,ucs_widget_module,xlite_module)。本地保存的原始 bundle 为 13,333,329 字节，SHA-256 为 `4939333d6a81876631d6acb65573884f5f750897b9a94295f5766aaf36892a73`。

`Opb` 构造当前 `streamAssistRequest` 时，将所选 level 放入：

```json
{
  "generationSpec": {
    "thinkingConfig": {"thinkingLevel": "<observed target level id>"}
  },
  "assistGenerationConfig": {"modelId": "<selected model id>"}
}
```

这条路径经过 `Epb`、`Dpb`、`As`，最终以 `widgetStreamAssist` 方法发送。`thinkingLevel` 与 `modelId` 同时保存在不同容器，不能因为公开 REST 文档将 `assistGenerationConfig` 标为弃用就自行合并。

level 不是此 bundle 中写死的 Gemini `LOW`/`HIGH` 枚举。`gVb` 从 agent card 的 `capabilities.extensions[]` 选择 URI 为 `https://www.googleapis.com/gemini-enterprise/a2a/extensions/model/v1` 的扩展，`fVb` 再读取其 `params.supportedThinkingLevels[]` 的 `id/name/description/disabled` 及 `defaultThinkingLevelId`。界面将选中的 `id` 字符串原样传到上述容器。

本段证明公开 widget 存在这个路径，尚未证明 Business Gemini 4 Argon 是拥有此扩展的目标 agent，也未证明其可用值或参数效果。不能把其他 agent 的 level ID 套到 Argon，也不能按普通 Gemini 枚举猜测请求。

可复核的字符位置、字段计数及来源元数据见 [advanced-public-evidence.json](../../scripts/business-addon/advanced-public-evidence.json)。位置采用 Python `Path.read_text` 的 UTF-8 解码及换行规范化后 Unicode 字符索引；不能直接当作原文件字节偏移。

## 目标元数据的只读获取线索

bundle 的 `eCc` 设置基地址 `<resolved-origin>/v1alpha/locations/<location>`，`r4a` 以 POST 调用 `/<method>`。Business 目标必须继续使用已确认的 `https://biz-discoveryengine.googleapis.com`、实际 location/config，而不是切到另一个服务尝试同一凭据。

除 `lookupWidgetConfig` 外，下面方法先经过 `_.us` 包装为 `{configId, additionalParams:{token,origin}, ...request}`。token 是现有认证机制的一部分，不写到报告；不因源码内置重试策略而照搬重试。

| 只读方法 | 请求字段的源码证据 | 需要检查的响应 |
| --- | --- | --- |
| `lookupWidgetConfig` | 特殊请求体 `{widgetConfigId,modelInfoView:"END_USER_WEB",view:"WITH_AVAILABLE_CONNECTORS"}`，不经过 `_.us` | `anonymousWidgetConfig` 中实际返回的模型和能力信息 |
| `widgetListAvailableAgentViews` | `listAvailableAgentViewsRequest:{pageSize:48,pageToken:"",searchQuery:"<target>"}`；界面还可传 `agentOrigin/sortBy/filter` | `listAvailableAgentViewsResponse.agentViews[]`；其 `a2aAgentInfo.agentCard.capabilities.extensions` |
| `widgetGetAgentView` | `getAgentViewRequest:{name:"<observed agent name>",showAgentDefinition:true}` | `getAgentViewResponse.agentView` |
| `widgetGetAgent` | `getAgentRequest:{name:"<observed agent name>"}` | `agent.a2aAgentDefinition.jsonAgentCard`，解析 JSON 后检查 capabilities/extensions |

`widgetSearchAgents` 另有 `searchAgentsRequest` 的界面分支，但无需同时调用多个列表入口。最小调查顺序是读已有 config，然后有界列表过滤目标；只有响应给出确切 name 且需要补充 card 时才读取该项。不存在目标或支持列表为空应作为有意义的否定结果保留。

源码位置：`_.us` 3895369、`getAgentRequest` 3906799、`Bs` 列表入口 3910280、`lookupWidgetConfig` 请求 3915286、`cmb` 界面列表请求 4716340、`_.Nv` 详情参数 4718278、`eCc` 6463250。

## 其他高级参数仍没有新的路径证据

[公开 streamAssist](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist) 的 `GenerationSpec` 字段表仍只有 `modelId`；[AssistGenerationConfig](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/AssistGenerationConfig) 同样没有采样、schema 或 system 字段。[Query](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/Query) 是当前用户输入合同，不提供独立的 system role。

固定 bundle 的 `systemInstruction`、`responseSchema`、`responseMimeType` 字面量均为零次；唯一 `temperature` 命中位于 MCP `sampling/createMessage` 的 schema，和 widget 生成参数构造无关。零次命中不能证明所有上游版本都不支持，但不能据此激活任何猜测映射。自定义 agent 的持久配置、Gemini 标准 API 的同名字段也不能代替当前 widget 的请求级语义。

既有 `runtime/business-local/live-parameter-probe-envelope.json` 已记录 `assistGenerationConfig` 下 `temperature/topP/topK/maxOutputTokens/responseMimeType/thinkingConfig` 六项逐一 Unknown name、HTTP 400。原始诊断没有开启映射。应保留这项拒绝证据，不重复盲测，也不把服务接受但忽略可选参数的 HTTP 200 解释为生效。

## includeThoughts 可做本地输出过滤，不能冒充上游控制

[Gemini ThinkingConfig](https://ai.google.dev/api/generate-content#ThinkingConfig) 将 `includeThoughts` 定义为响应是否包含可用的 thoughts；`thinkingBudget` 和 `thinkingLevel` 是不同设置。Python 2.32.0 的 `types.ThinkingConfig` 描述与此一致。JavaScript 2.22.0 的 `response.text` getter 本身跳过 `thought:true` 的文本，但完整 `candidate.content.parts` 仍保留；仅检查 `.text` 无法证明响应过滤成功。

因此可实现明确标注的**本地输出投影**：false 隐藏原生 `thought:true` Parts，true 只保留实际上游提供的内容，不生成或推断 thoughts。它不减少上游思考、不能证明预算/深度控制，也没有 token 计费或 usage 变化保证。缺省策略需另行明确，不能从“已忽略参数”推导出当前已经实现过滤。

实现至少要同时处理这些边界：

- 校验和资源预算针对原始完整上游输出，隐藏内容不能绕过 Part/字节限额。
- 删除的是整个 thought Part。剩余 Parts 及签名保持原值，不把被删除 Part 的签名挪到其他文字、代码或媒体上，也不制造空 text 或签名占位符。
- 成功前缀必须按实际交付的投影计算。当前 Core 按原始 `output` 计算 `ModelHistoryDigest`；只在 handler 过滤会导致 SDK 自动续聊得到 409。投影选项还须纳入 OptionsHash，避免不同呈现策略复用错误前缀。
- grounding 的 `partIndex` 必须按完整可见序列重映射，指向隐藏 Part 的支持关系应删除；不能直接沿用原始索引。
- unary、SSE、JSON 数组流采取同一投影；流式保留最后一个真实可见 Part 到 STOP。全部内容被过滤时，不得为满足 SDK Chat 伪造成功内容。
- 离线夹具应给出真实 thought Parts，再验 false 被过滤、true 保留、签名及 grounding 关系和 Chat 自动续聊。真实调用如未返回 thoughts，只能记录“未观察到”，不能用零 thought 证明过滤有效。

## 有界验证方案

先只读取得目标支持的 level ID，再由主窗口决定是否发起一项显式 opt-in 探测。每次进程最多一次生成，固定已授权 origin/模型，合成输入，无重试、不跟随重定向；不自动移除拒绝字段重发。记录发送位置、已观察 ID 的来源、HTTP/终态和固定拒绝分类，不留凭据、响应正文或动态异常消息。

HTTP 200 只能证明服务器接受该结构，不能证明思考强度有效；缺少可重复的目标语义证据时，能力状态继续保持“路径已观察、效果未核实”。上述 thinking level 探测未执行，也没有执行已被 Windows Application Control 阻断的测试包。

后续主窗口只读核对的目标配置给出 `WEB_GROUNDING_TYPE_GOOGLE_SEARCH`，但没有目标 `supportedThinkingLevels`，因此本轮只启用空 Google Search 工具。`scripts/business-addon/advanced_live.py` 提供独立、显式 `--allow-live` 的本地验收：默认仅打印计划，不读取账号；实测最多两次生成（流式首轮和 SDK Chat 精确历史续聊），六项本地 400 预检及一次预期 501 的 countTokens。它保留来源、support、UTF-8 范围和官方 PEP 8 来源的各项结果；只有来源而没有支持片段时明确标记 `source_only`，不宣称完整 grounding 验收通过，也不据此断言没有执行检索。报告流式文本帧的首次到达和到达跨度，不保存响应原文或凭据。

runner 请求中的 temperature、maxOutputTokens 和 thinkingConfig 仍只作为接受但未映射的可选参数记录，不声明语义生效，也不依赖 maxOutputTokens 限制费用。固定 Python SDK 2.32.0 实际将 thinkingConfig 子键序列化为 `include_thoughts` 和 `thinking_budget`；纯 MockTransport 检查据此核对 0/false 保真。真实调用由主窗口在重启后统一执行。
