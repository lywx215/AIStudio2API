# 第四阶段协议证据与实施边界

核对日期：2026-10-22（Asia/Shanghai）。本记录只核对公开官方文档、已归档方案及当前源码，没有读取账号凭据、访问已登录 Google 会话、发起生成或执行 Host/API 测试。它是实施依据，不是第四阶段运行验收报告。

## 移交来源

本机固定 R3 包位于 `C:/Users/lywx2/.codex/visualizations/2026/10/22/01a161af-e6bf-7c31-85f2-6a38d25b9e38/gemini-business-research/development-package-r3`。

- `contracts-r3-draft.txt` 第七节：默认新会话；显式复用须匹配主体、完整前缀、账号、config、模型 revision、epoch，并使用单写租约。已提交但结果不明时不能盲目重试。
- `tasks/G04.txt`：保留工具/schema/思考签名并设置能力门禁，函数往返及签名归属不能丢失。
- `tasks/G06.txt`：高级参数逐项激活属于条件任务，必须先取得真实效果证据，HTTP 200 本身不足以通过。
- `tasks/Q09.txt`：不得伪造签名、模型版本或 token；明确区分原生、模拟、未支持、未核实。

上述包有原始 R3 阶段编号；没有找到独立的后续“第四阶段”详细文件。当前后续阶段范围来自仓库 [第三阶段说明](phase3-observation-collection.md) 的后续章节。读取原研究 thread 返回 ephemeral thread 不支持 turns/list，因此未把未读取的内容当作移交证据。

旧任务卡中不兼容可选参数一律拒绝的要求，已由用户后来要求“忽略不兼容可选参数”覆盖，见 [可选参数策略](optional-parameters.md)。消息内容、角色、函数调用/响应不能因该策略被静默删除。

## 官方合同与可采用的结论

| 项目 | 核对结论 | 实施边界 |
|---|---|---|
| 会话续接 | Enterprise `streamAssist.session` 指向已有会话时读取历史，并存储本次 query/answer。`query` 是当前用户输入；未提供任意 user/model 历史数组入口。 | 可以研究并实测复用真实 session；不能据此声称支持任意客户端历史导入。 |
| 参数 | `AssistGenerationConfig` 已弃用；替代 `generationSpec` 目前同样只列 `modelId`。 | 没有找到采样、schema、thinking 的新字段合同，不应批量启用猜测映射。 |
| 工具 | Enterprise `toolsSpec` 选择工具注册表、搜索、图像、视频、地图等功能；它不是 Gemini 自定义函数声明数组。 | 不能直接将 `tools/functionDeclarations/toolConfig` 改名发送到这里，也不能从工具结果文本伪造 functionCall。 |
| system | 已核对的 Enterprise 请求及 Query 合同没有 Gemini `systemInstruction` 的独立字段。 | 不将 system 拼入 query 伪装为原生语义；本轮无可直接开启的映射。 |
| 输出代码 | Enterprise `AssistantContent` 有 `executableCode` 与 `codeExecutionResult`，前者说明目前为 Python；结果有成功、失败、期限结束等枚举。 | 有依据增加保真输出适配和合成合同测试；不在本机执行返回代码，不宣称已经实测触发。 |
| 输出引用 | Enterprise 使用 `textGroundingMetadata` 与 `citationMetadata`；文本定位以 UTF-8 字节计算。 | 与 Gemini candidate 的 grounding 结构并非同形，转换前必须验证索引/文本/引用关系，不能整对象改名。 |
| 思考签名 | Enterprise 公共 `AssistantContent` 列出 thought；本次页面没有 thoughtSignature 字段说明。 | 已有透传行为不能作为支持生成/恢复签名或工具回合的证据；不伪造或重签。 |
| token 计数 | Gemini `countTokens` 使用所选模型 tokenizer，并可覆盖整个生成请求，包括 system/函数定义。 | 文件摄取 tokenCount 不等价，继续不冒充原生 countTokens/usage。 |

Enterprise 上述公开文档描述 `discoveryengine.googleapis.com` API；本地 Core 调用网页的 `biz-discoveryengine.googleapis.com/.../widgetStreamAssist` 封装。两者字段相似只是实施线索，不能用公开 API 文档代替该账号、该 widget 路径的真实验收。

官方来源（本日读取）：

- [Enterprise streamAssist](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist)：session/query、toolsSpec、generationSpec；页面标示更新于 2026-10-09。
- [AssistGenerationConfig](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/AssistGenerationConfig)：仅 modelId 及弃用说明。
- [Enterprise Query](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/Query)：当前用户 query 与 QueryPart 合同。
- [Enterprise AssistAnswer](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions.assistAnswers)：AssistantContent、代码结果、grounding、citation 与字节索引。
- [Gemini generateContent](https://ai.google.dev/api/generate-content)：contents 历史、systemInstruction、tools 与 generationConfig 的目标标准接口语义。
- [Gemini countTokens](https://ai.google.dev/api/tokens)：模型相关计数及完整请求计数。

## 建议的原生续接最小范围

以下为工程建议，不是已通过声明。

1. 首轮仍创建新 session。只有上游明确成功、响应已完整交付后，才登记客户端实际获得的完整前缀；状态仅保留于有界、带 TTL 的内存中。
2. 后续标准 Gemini 请求必须是严格 user/model 交替、最后一个 user 为新增回合。历史必须逐角色、逐 part、逐原始值匹配已完成前缀；只有当前新增 user 内容发送给上游。
3. 缓存范围至少包含已验证模型 Key 的稳定 principal、公开模型/实际 modelId/Auto 模式、绑定语义摘要或 revision、账号 ID、config ID、credential epoch。不可仅用 prompt 哈希跨账号找“相似会话”。
4. 同一上游 session 同时只允许一个写入者。续接成功后旧前缀不再是可写入口；并发分叉应明确失败，不能串行追加两条互不相干的分支。
5. 缓存缺失、重启、过期、前缀改写、账号/模型绑定变更须在生成前返回可识别错误。不可通过重放历史生成、拼接 prompt、隐式新建 session 或换账号来“恢复”。
6. 已提交生成发生取消、断流、错误或终态不明时，停止该 session 的续接资格；即使客户端没有收到文本，上游也可能已追加 query。不得自动重试。
7. 先以纯文本完成两至三轮实测。文件历史、输出媒体、thought/signature、函数回合须分别保留完整内容边界并取得证据；没有完整处理时应明确缩小可续接范围。
8. 续接涉及缓存生命周期和账号租约，至少需覆盖不同 principal、账号 epoch 变更、绑定变更、前缀篡改、并发分叉、取消、过期、上限回收与未知历史零生成测试。Host/API 两项继续依用户要求留为未验收，不能用新包测试代替它们。

## 建议的真实验收

采用合成随机标记：首轮要求记住标记但只输出固定确认；第二轮请求回忆标记，第二轮上游 wire 中不得重传首轮标记；第三轮用流式继续已有上下文并验证 STOP。记录本地 handler/SDK 结果、真实上游会话一致性与仅最新 query 发送的脱敏计数，不能仅凭模型答对认定 session 复用实现正确。

任意历史和跨 principal 的负向检查使用假上游或确认本地拒绝，避免把错误测试变成无必要的真实生成。对参数仍按“有无转发”“上游有无接受”“效果有无验证”分别记录。此前六个字段已得到上游 Unknown name 的证据，不重复请求同一组来代替实现。
