# Business 原生 token 计数与生成用量证据

核对日期：2026-10-22。范围为官方公开文档、公开 widget 脚本、两份公开 Discovery schema、项目固定版本 SDK 和既有脱敏报告。本次未读取账号凭据，未发起计数、文件上传或生成请求，未执行应用测试包。

结论：**尚未核实可用现有 Business 会话调用的原生 `countTokens` 方法，也未找到可准确映射为 Gemini `usageMetadata` 的 Business 生成字段。** 因此本地已启用模型的 `:countTokens` 应继续明确返回 `501 UNIMPLEMENTED`，生成响应继续省略未知用量；文件接收计数、字符估算和其他模型 tokenizer 都不能填充原生用量。本结论限定于下面的证据范围，并不证明私有服务永远不存在其他方法。

## 1. 三种接口的地址和身份不能混用

| 接口 | 已核实方法与输入 | 已核实结果 | 认证边界 |
| --- | --- | --- | --- |
| 当前 Business widget | `POST https://biz-discoveryengine.googleapis.com/v1alpha/locations/global/widgetStreamAssist`；外层 `configId`、`additionalParams`、`streamAssistRequest`，内层包含 `session`、`query`、模型配置及可选 `fileIds` | 流中 `streamAssistResponse.answer.replies/state`，另有会话标识；未核实计数结果 | 当前实现使用 Business 会话产生的 Bearer；源码指定 JWT issuer 为 `https://business.gemini.google`、audience 为 `https://biz-discoveryengine.googleapis.com`。没有证据授权它访问另两类 API，也未核实 widget 专用 OAuth scope |
| Gemini Developer API | `POST https://generativelanguage.googleapis.com/v1beta/models/{model}:countTokens`；`contents[]` 或完整 `generateContentRequest` | `totalTokens`，可带缓存计数和输入模态明细 | 官方示例使用 Gemini API key；固定 SDK 使用 `x-goog-api-key`。需要该服务可用的模型名称，不能因本地显示名相同而认定可用 |
| Gemini Enterprise Agent Platform 的模型 API | `POST https://{location}-aiplatform.googleapis.com/v1/projects/{project}/locations/{location}/publishers/google/models/{model}:countTokens`；`contents[]` | `totalTokens`、`totalBillableCharacters`、`promptTokensDetails[]` | 文档示例使用 Google Cloud OAuth access token；固定 SDK 的 Vertex 路径要求 `https://www.googleapis.com/auth/cloud-platform` scope，并选择项目、地区、发布者模型 |

Developer API 的输入、响应和 API key 用法见 [models.countTokens](https://ai.google.dev/api/tokens)。Agent Platform 的精确地址、请求体及 Cloud Bearer 用法见 [CountTokens API](https://docs.cloud.google.com/gemini-enterprise-agent-platform/reference/models/count-tokens)。后者文档名称含 “Gemini Enterprise”，其 HTTP origin 仍是 `aiplatform.googleapis.com`，不等同于网页 widget。

当前 widget 地址、请求体、固定操作白名单和 JWT audience 的本地证据在 `internal/addons/business/core/engine.go`：`baseURL`、`generate` 内请求构造、`postEncoded`、认证 payload。这里记录源码合同，没有读取或保存任何实际 token。

## 2. 公开 widget 脚本确实调用了什么

公开入口为 [Google widget loader](https://cloud.google.com/ai/gen-app-builder/client)，引用的 [版本化 widget bundle](https://www.gstatic.com/_/mss/boq-cloud-ai-ucs/_/js/k=boq-cloud-ai-ucs.UcsWidgetUi.en_US.pveFsgaw8rQ.2021.O/am=AACAERg/d=1/rs=AAj4Nmy6tdJplhe-n3Ngu_SbpIesx-rN-Q/m=bootstrap_module,ucs_widget_module,xlite_module) 已由并行研究缓存。本次只读同一文件并复核 SHA-256。

- 缓存：`runtime/business-local/advanced-public-widget-bundle.js`，13,333,329 bytes。
- SHA-256：`4939333d6a81876631d6acb65573884f5f750897b9a94295f5766aaf36892a73`。
- 来源记录：`scripts/business-addon/advanced-public-evidence.json`。
- 本次检索结果：`runtime/business-local/token-protocol-research/public-bundle-facts.json`。其中偏移为 .NET `ReadAllText` 原样读取后的 UTF-16 code-unit 索引，未做换行规范化；与来源记录的 Python Unicode 偏移不可直接混用。

实际检查的调用链：

1. `_.us` 构造 `configId`、`additionalParams.token/origin` 外壳；`As` 选择 `widgetStreamAssist`，读取 `streamAssistResponse`。`r4a` 将固定操作名追加到运行时注入的 `a.Ua`，使用 `POST`；`_.E4a` 设置 Bearer header。公开 bundle 自身未包含当前 `biz-discoveryengine` origin 字面量，具体 origin 的依据是本地已运行协议实现，不能从动态基址推导新端点。
2. `R4a` 选择 `widgetListSessionFileMetadata`，请求容器为 `listSessionFileMetadataRequest`；其调用方传 `name` 和可选 `filter`，结果为 `listSessionFileMetadataResponse`。文件列表映射中出现 `tokenCount:Number(n.tokenCount??0)`，与 `byteSize`、`quotaPercentage`、`fileId` 并列。
3. `tokenCount` 只有上述同一映射中的两个字面命中。`countTokens`、`CountTokens`、`count_tokens`、`computeTokens`、`usageMetadata`、`usage_metadata`、`promptTokenCount`、`candidatesTokenCount`、`totalTokenCount`、`inputTokenCount`、`outputTokenCount`、`totalTokens` 等检索均无命中。没有发现可复用的原生计数请求构造或生成用量消费位置。

这是一个具体版本的静态检索与调用点核对，不是服务端完整 API 清单；按需模块、服务端新增字段或未使用的响应字段仍可能未在该 bundle 中出现。未据此猜测 `widgetCountTokens` 或轮询候选路径。

## 3. 公开 Enterprise schema 的约束

本次无凭据下载的 [Discovery Engine v1alpha Discovery 文档](https://discoveryengine.googleapis.com/$discovery/rest?version=v1alpha) 保存为 `runtime/business-local/token-protocol-research/discoveryengine-v1alpha.json`，2,342,083 bytes，SHA-256 `75ce245c1a1beff4b1a9ab82e7c272f0e3fe22d14249815a54513864a1cbab7e`。

另外只读了已有公开缓存 `C:/Users/lywx2/Projects/Gemini-Enterprise-Manager/scratch/runtime/discovery-v1alpha-public-schema.json`，SHA-256 `2ced8d17d77df428068a8d95fa8aa8b568034a35109a959354ff5048fc7f354b`。两份文件编码/布局不同，本次关心的结果一致：revision 都是 `20261013`，递归枚举各资源共 386 个方法，**没有 `countTokens` / `computeTokens`**。存在的 `widgetConfigs.get/patch` 是配置资源操作，不是 widget 计数入口。提取记录在 `runtime/business-local/token-protocol-research/public-schema-facts.json`。

公开的生成方法是：

```text
POST https://discoveryengine.googleapis.com/v1alpha/{name=projects/*/locations/*/collections/*/engines/*/assistants/*}:streamAssist
```

该方法使用 `StreamAssistRequest` / `StreamAssistResponse`，需要 `discoveryengine.assistants.assist` IAM 权限，并允许以下任一 OAuth scope：`cloud-platform`、`discoveryengine.assist.readwrite`、`discoveryengine.readwrite`、`discoveryengine.serving.readwrite`（均以前缀 `https://www.googleapis.com/auth/` 开始）。这是公开 Cloud API 的权限合同，不应移植成 widget JWT 的 scope。见 [Enterprise streamAssist](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist)。

两份 Discovery schema 的 `StreamAssistResponse` 字段是 `answer`、`assistToken`、`connectorAuthErrors`、`invocationTools`、`invokedSkills`、`sessionInfo`；`AssistAnswer` 为 `name`、`state`、`replies`、`connectorDisplayNames`、`assistSkippedReasons`、`customerPolicyEnforcementResult`。均未声明生成 token 计数或 `usageMetadata`。

网页 REST 文档还列出 `diagnosticInfo` 等 Discovery 快照没有包含的字段，说明这两类公开资料不完全同步。`diagnosticInfo` 被明确定位为供人工排错、结构可随时改变的对象，不能把其中任意数字猜成计费或模型用量。`assistToken` 是本次请求与响应流的标识，也不是 token 数。见 [AssistAnswer](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions.assistAnswers) 和上述 `streamAssist` 文档。

全 schema 中命中的 `AlphaEvolveExperimentStats.inputTokenCount/outputTokenCount` 属于实验统计，`Notebooklm.SourceMetadata.tokenCount` 属于来源内容；这些均不在 widget 本次生成响应路径上。公开快照甚至未包含已知的 `AddContextFileResponse`，因此“schema 中未发现”仅是证据边界，不能据此断言私有方法不存在。

## 4. 为什么文件计数不能替代原生计数或 usage

官方 [sessions.addContextFile](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions/addContextFile) 的 `tokenCount` 是文件长度计数，使用 int64 的 JSON 字符串形式，且已标记弃用：部分文件类型不会返回，将来可能移除。它不是带指定生成模型、完整历史、system instruction、工具及缓存的 `countTokens`，更不是生成响应的账单用量。

既有脱敏证据 `runtime/business-local/long-context-20261022-argon/input-count.json` 记录上传字节数 370,092、文件接收计数 153,363，来源准确标为 `widgetAddContextFile.addContextFileResponse.tokenCount`，并注明该步骤没有发送生成请求。这个数字只证明对应上传文件的计数结果，不能宣称是 Argon 本次生成的 `promptTokenCount`，也不能将生成后的文本再次上传后得到的数字冒充 `candidatesTokenCount`。

标准 Gemini [UsageMetadata](https://ai.google.dev/api/generate-content#usagemetadata) 区分输入、缓存、候选输出、工具输入、思考及总量。准确适配需要每个源字段的含义、缺省规则、单位，以及流中增量或累计规则；当前证据没有给出 widget 对应字段。未知值应省略，不能写 `0`，也不能用拼接长度或文件计数填充。

## 5. SDK 交叉核对和实施决定

项目固定 `@google/genai` 版本为 `2.22.0`（`scripts/business-addon/sdk-js/package.json`）。只读 `node_modules/@google/genai/dist/node/index.mjs` 可核对：

- `countTokens`（当前文件约 16483 行）在 Developer 与 Vertex 两个分支均发送 `POST {model}:countTokens`；没有 widget 分支。
- 基址选择约 13539–13559 行：`aiplatform.googleapis.com` / 地区基址，或者 `generativelanguage.googleapis.com`。
- 认证常量约 23768–23769 行：`x-goog-api-key` 和 Vertex `cloud-platform` scope。
- SDK 存在方法、可以把 `baseUrl` 指向本地服务，并不能使底层 Business widget 获得该能力。此处没有执行 SDK 请求。

本轮不增加推测性的 Core usage 解析器，也不更改凭据 origin、允许列表或模型映射。`gemini/handler.go` 对已知模型的非生成方法保留 `501 UNIMPLEMENTED`；模型能力目录不宣称原生计数，生成响应不估算 `usageMetadata`。

将来启用的最低证据要求是：取得该 Business 上游真实方法的精确 URL、请求/响应字段和现有身份可用性；计数必须绑定准确模型及完整有效输入；生成用量必须确认流式累计/增量和终态语义，再做严格字段与整数边界测试。仅有另一个 Google 产品的同名方法、截图方法列表、未知 `diagnosticInfo` 或文件 token 数，均不足以冻结实现合同。

本次验证为公开资料与源码只读核对。没有运行 Host/API/resources/app/gemini/config/files/token-count 测试，没有发起模型调用；原有系统策略导致的未验收记录保持不变。真实本地 `countTokens` 的 `501` 兼容检查由主窗口另行归档，不将研究结果冒充 live 验证。
