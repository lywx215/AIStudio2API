# Business 工具请求协议证据

核对日期：2026-10-22。本记录只读官方文档、公开 widget JavaScript 与本地源码，不读取登录态或凭据，不发起模型调用。请求字段存在、服务端接受和工具实际执行是三个不同的验收结论；本记录不将它们合并。

## 可验证的搜索开关

官方 [Enterprise streamAssist/ToolsSpec](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist#ToolsSpec) 定义 `toolsSpec.webGroundingSpec`，空对象即可请求启用网页检索。该字段依赖 Assistant 的 `webGroundingType`，可以使用 Google Search，也可以使用 Enterprise Web Search。它不是指定 Google 搜索供应商的请求字段。公开的 `WebGroundingSpec` 没有时间范围、搜索种类或强制调用参数。

Google 公开 widget bundle 的请求构造器与文档一致：

| 位置（下列缓存版本的字符偏移） | 观察与含义 |
|---|---|
| `Opb`，4907213 起 | 请求以 `session/query/filter/fileIds` 开始，工具开关写到同级 `toolsSpec`；启用网页搜索时设置 `p.webGroundingSpec={}`，否则移除这个键 |
| `LO`，6520420 | `search_web` 分支只产生 `{webGroundingSpec:{}}`；无需同时启用注册表、企业数据检索、图像、视频或地图 |
| `Epb`，4904838；`Dpb`，4915930 | 上述对象通过 `streamAssistRequest` 进入 widget 流式调用链 |
| `widgetStreamAssist`，3901436 | 传输层用该方法名提交请求；此证据不是另一个 OAuth 上游的请求示例 |
| 搜索能力判断，3995014 | 代码区分旧 `googleSearchGroundingEnabled` 与 `WEB_GROUNDING_TYPE_ENTERPRISE_WEB_SEARCH`，与官方两种供应商的区别一致 |

代码默认工具组合还包含 `default_tool_registry`、企业检索、图像、视频、canvas 等字段。本地适配不能因为它们与网页搜索出现在同一分支，就默认打开全部工具。

官方 [Assistant](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants#WebGroundingType) 与 [WidgetConfig.AssistantSettings](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.dataStores.widgetConfigs#AssistantSettings) 可用于只读核实搜索供应商。`UNSPECIFIED`/`DISABLED` 不代表可用；页面默认开关也不是请求已执行检索的证据。本研究不读取实际账号配置，真实账号的开关、权限、供应商和调用结果由主窗口另行核对。

## Gemini 可转换范围

官方 [Gemini Tool](https://ai.google.dev/api/generate-content#Tool) 中的 `googleSearch` 指 Google Search，`codeExecution` 指模型执行代码，`functionDeclarations` 是客户端函数声明。三者不能仅以“工具”这个名称互换。

| Gemini 输入 | 有依据的 Business 转换 | 边界 |
|---|---|---|
| `tools:[{googleSearch:{}}]` | 经确认目标 Assistant 使用 Google Search 后，可转换为 `streamAssistRequest.toolsSpec.webGroundingSpec={}` | 只证明请求允许检索；模型不一定选择调用。不凭 HTTP200 宣称真正检索 |
| `googleSearch.timeRangeFilter/searchTypes` | 暂无已核实字段 | 不删掉限制后宣称完整支持；不要把图片搜索当作图像生成 |
| `googleSearchRetrieval.dynamicRetrievalConfig` | 暂无已核实字段 | 旧版检索配置不能直接当空 `googleSearch` |
| `codeExecution:{}` | 未发现独立请求开关 | 保留已有真实 `executableCode/codeExecutionResult` 输出适配；不伪造 `codeExecutionSpec` |
| `functionDeclarations/toolConfig/functionCall/functionResponse` | 未发现 Gemini 客户端函数往返合同 | 服务器内部 Agent 工具调用展示不等于把函数执行交给本地调用者；不能自行执行、把调用转成普通文本或自动生成重放 |
| `fileSearch/urlContext/googleMaps/mcpServers/computerUse` | 本轮未冻结映射 | 资源归属、权限与参数不相同；不能开启 Enterprise 数据库、注册表或连接器来假装兼容 |

本轮冻结空 `googleSearch` 的最小转换，要求实际租用账号每次只读核实 Google 供应商；未确认时不降级为另一种搜索。保留现有未知可选项处理策略，不通过提示词注入实现工具。根选项的 `OptionsHash` 已覆盖工具输入，继续保证会话不能在工具配置变化时错误续接。供应商核实结果不缓存，避免账号 epoch 或管理端配置变化后沿用陈旧值。

## 代码执行与函数调用的否定边界

本次公开 bundle 中 `codeExecutionSpec`、`codeExecutionEnabled`、`enableCodeExecution`、`functionDeclarations` 均为零命中。`codeExecution` 的四处命中属于 AgentFlow 的 `codeExecutionNode` 导入/导出或返回内容筛选；不是普通聊天请求开关。官方 Enterprise `ToolsSpec` 也未列代码执行字段。零命中不能证明 Google 永久不支持，但足以阻止本地凭名称猜字段。

Bundle 中存在 `functionCall/functionResponse`，其字段包括内部 `functionName/functionId/functionCallId`，用于展示服务端动作与结果。Gemini 的客户端函数声明、函数调用 ID、函数响应轮次及调用取消/失败语义尚无一一对应证据；本地不能把这些内部动作输出为等待客户端处理的 Gemini `functionCall`。

之前实测能返回代码与执行结果，只证明输出路径可用。它不证明 `tools:[{codeExecution:{}}]` 改变了上游开关，也不证明可禁止后台工具执行。

## 复现来源

- 本地公开 loader：[advanced-public-widget.js](../../runtime/business-local/advanced-public-widget.js)，SHA256 `481aeccaf1e775461833093518284d21f3b55a864e881f3f891f293fc84ae97d`。
- 公开 [Google widget bundle](https://www.gstatic.com/_/mss/boq-cloud-ai-ucs/_/js/k=boq-cloud-ai-ucs.UcsWidgetUi.en_US.pveFsgaw8rQ.2021.O/am=AACAERg/d=1/rs=AAj4Nmy6tdJplhe-n3Ngu_SbpIesx-rN-Q/m=bootstrap_module,ucs_widget_module,xlite_module)，由 loader 的 `axVgKc` 静态反转义得到；[本地缓存](../../runtime/business-local/advanced-public-widget-bundle.js) SHA256 `4939333d6a81876631d6acb65573884f5f750897b9a94295f5766aaf36892a73`。字符偏移仅适用于此版本；bundle 由主窗口下载，本子代理只读检查。
- [Google Discovery v1alpha 缓存](../../runtime/business-local/token-protocol-research/discoveryengine-v1alpha.json)：`GoogleCloudDiscoveryengineV1alphaStreamAssistRequestToolsSpec` 列出 image/video/vertexAiSearch/webGrounding；其代码执行类型为返回用 `AssistantContentCodeExecutionResult`。Discovery 快照、网页文档与私有 widget 能有版本差异，不能任选一个作为全部字段清单。
- 本地 `business-gemini-pool/model_routing.py` 的 `search_enabled→webGroundingSpec` 仅作检索线索；最终请求映射依据是上述 Google 公开代码和合同，不是第三方模拟。

## 验收要求

最小离线检查应覆盖空开关的精确请求字段、未请求时不自动启用工具、不复制其他默认工具、供应商不匹配时不冒充 Google、工具选项变化的会话隔离。真实验收需要分别记录本地接收、上游字段接受、上游明确工具/grounding 证据；普通回答或提示词中的“已搜索”不能替代后者。

## 本轮已实施的最小转换

主窗口只读请求 `lookupWidgetConfig` 得到 HTTP200，当前账号的 `anonymousWidgetConfig.assistantSettings.webGroundingType` 为 `WEB_GROUNDING_TYPE_GOOGLE_SEARCH`；脱敏证据见 [advanced-read-config.json](../../runtime/business-local/advanced-read-config.json)。这证明账号供应商配置，不证明生成时已经执行检索。

Core 的 `requireGoogleSearch` 在 `Request.GoogleSearch=true` 时，于同一账号租约的 JWT 准备完成后、创建或继续 session 及上传文件前，向固定 Business API origin POST `lookupWidgetConfig`。请求只包含 `widgetConfigId` 与 `modelInfoView:"END_USER_WEB"`，不传 Cookie，不跟随跳转，不重试。响应读取上限1MiB，拒绝重复键、非法UTF-8/代理项及非对象结构，按精确大小写读取路径。缺失、禁用或其他供应商返回固定400；HTTP错误沿用既有认证、权限、配额与瞬时失败分类；结构或大小异常返回固定502，不泄漏响应内容。诊断阶段为 `config_lookup`。

核实成功后，生成请求仅增加 `toolsSpec:{webGroundingSpec:{}}`，不打开其他工具。普通请求不读取该元数据，也不增加 `toolsSpec`。调用者取消时终止预检，不创建session或提交生成。供应商不支持属于请求能力限制，不将账号标记为需要重新登录。

正常 Core 专项 `go test -mod=readonly ./internal/addons/business/core -run '^TestGoogleSearch' -count=1 -json` 已通过7个顶层测试、32个子例，覆盖最小字段与同JWT、无默认开关、供应商变化、HTTP分类、取消、响应关闭和精确1MiB边界；[日志](../../runtime/business-local/advanced-google-search-core.jsonl)。本子代理没有执行真实模型调用。Host/API/resources/app/gemini/config/files/token-count 的 Application Control 未验收记录继续保留，不能用上述 Core 专项或历史通过替代。
