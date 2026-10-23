# 高级参数、搜索工具与原生计数核实

本次定向开发跨2026-10-22至23，沿用本地一个已有账号及 `business-gemini-4-argon`。三个内部子代理分别核对工具协议、高级参数/SDK、计数及错误边界，主窗口负责共享合同、解析、集成、重启及真实验收。没有启用外部审核、部署或改变系统策略。

**最终限制：最新thought偏移修复构建被Windows Application Control事件3077阻止启动，最后验收0次模型调用。** 已恢复上一版可运行构建，地址仍是 `http://127.0.0.1:2048`，配置revision8及原账号保留。源代码/104项Core+contract检查和10项SDK离线检查通过，但最新修复尚未实测，当前运行版本在同ID包含thought时仍仅返回来源。此前4次真实生成均200并完成续聊，严格grounding验收未通过的两份报告原样保留。

## 搜索请求的最小兼容范围

标准 `tools:[{"googleSearch":{}}]`（及 `google_search` 别名）新增真实映射。Core 在同一个账号租约内使用现有 JWT，只读查询 `lookupWidgetConfig`，仅接受精确的 `WEB_GROUNDING_TYPE_GOOGLE_SEARCH`，之后发送 `streamAssistRequest.toolsSpec.webGroundingSpec={}`。普通请求不查询工具配置，也不会自动启用其他工具。供应商不符返回400，配置传输和协议错误按真实类别处理；不重试、不缓存过期能力、不自动去掉工具重发。

本地账号配置已实际返回 Google Search 供应商。公开 widget 代码及来源说明见 [工具协议证据](tools-protocol-evidence.md)；这比单凭同名字段或第三方模拟代码确认映射更具体。

只支持空配置。Google Search 时间范围/搜索类型限制、同一 Tool 中混合其他字段，或非空共享 `toolConfig` / `tool_config` 时，整个搜索变体沿用未映射可选项的忽略策略，不丢掉限制后扩大搜索。重复搜索、冲突别名、非法类型明确400。函数声明、函数执行交还、代码执行请求开关、其他工具仍没有已验证映射；函数调用/响应消息继续拒绝，已有真实代码结果只按输出合同传递。

工具输入和共享配置继续参与历史 OptionsHash，变化不能错误续接旧session。搜索本身是否执行，需要结构化来源及支持片段作证；HTTP200、正文链接或答案自述都不能代替。

## 延迟引用的新证据

首轮标准接口测试实际有官方 PEP 8 来源，但零支持片段，严格验收记录保留 `passed=false`。两次生成均200并STOP，文本/完整SDK历史续接成功；不能把这些局部成功写成搜索完整验收通过。

随后只读获取同一合成session的完整答案，没有新增生成：正文、inline附加内容、仅引用消息都有相同 `replyId`；4条segment的指定UTF-8字节范围及text均精确对应该正文。Core此前仅转换同一groundedContent内的正文与引用，因引用消息没有content而只保留来源。这项新证据允许按明确replyId补充关联，无需猜最近文本。

实施要求：只关联非空、有效且有界的相同replyId；基于实际已交付Part的映射处理累计终帧，不能重复累加；延迟片段须有明确text和精确offset证据。无ID、不同ID、普通正文带签名、代码/其他媒体、缺少对应text或不能精确定位时不产生猜测支持关系。无注解的suggestions附加Part不参与文本offset。元数据先保留来源，延迟关联在完整流成功结束后统一处理，后续身份歧义不能留下早先加入的错误支持关系。

首次修复复测仍严格未通过：此次上游返回thought文本，同ID整体保守失效。再次只读回查证明，3条segment在582字节可见正文全部精确匹配，在177字节thought中零匹配。因此仅将真正的thought文本排除出正文offset；原thought Parts及其签名完整交付，不能删除或迁移签名。实际global Part索引仍包含它们，引用只指向普通正文。只匹配thought而不匹配正文的引用不输出；这项处理没有实施includeThoughts过滤，也不改变上游思考参数。

相同SHA的公开widget代码也采用这一语义：`HLa`只将普通正文纳入按内容身份归并的UTF-8定位，`FLa`将thought和代码内容单独处理。原缓存使用.NET UTF-16索引时，HLa为2173699、FLa为2175921，相关ILa为2173481。网页实现还允许缺省片段text和归一化空白，本地没有采用这种放宽；延迟引用始终要求明确text逐字节一致。

跟踪上限为每ID1024字节、256个ID、ID合计64KiB、8192个定位区间、4MiB可锚文本；待关联metadata合计512KiB、512份，累计组装/验证16MiB和131072次区间检查。实际输出仍受原有4MiB、4096 Parts及共享32MiB生成预留预算约束，最终由Gemini输出校验器复验字节范围。无法确认的内容保留source-only，超资源限额明确失败，不制造空文本或成功终态。

完整运行结果及是否已验收以 [validation最新记录](validation.md) 和本轮机器汇总为准。

## 高级参数与计数的真实边界

[高级参数研究](advanced-parameter-evidence.md) 发现 widget 使用独立的 `generationSpec.thinkingConfig.thinkingLevel`，ID来自目标agent扩展，不是任意套用LOW/HIGH。只读当前Argon目录和agent列表没有获得可用level ID，所以没有启用该映射。已有六项参数在旧容器下的真实400记录保留，不再次盲测。当前唯一启用模型的 generation parameter映射为空；本轮SDK携带的0/false及思考选项仅用于验证兼容接受，不表示转发或生效，也未添加本地思考过滤。

[计数研究](token-count-protocol-evidence.md) 交叉核对公开widget、两版Discovery、官方文档和固定SDK，没有找到可用当前Business身份调用的原生countTokens或生成usage合同。本地SDK `count_tokens` 实际返回501，响应未填usage；文件摄取tokenCount不替代原生计数。另一个Google模型API的同名方法和凭据不能移植到本widget。

## 验收范围

Host/API/resources/app及既有被Application Control阻断的Gemini/config/files/token-count测试保持未验收，本轮不启动、改名或搬动它们。新增Gemini解析及Host状态断言可做编译/静态核对，不能因此记为单测通过。可运行的Core/contract、受限SDK检查、真实标准接口结果分别记录。前端本轮没有变更，历史154项通过仍只作为历史证据。

原58项任务、项目级 `live_verified=false` 和外部环境验收边界保持；新的搜索子集完成也不表示函数调用、高级参数或原生计数一并完成。
