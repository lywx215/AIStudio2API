# Business 本地 token 用量

2026-10-23：按当前用户要求接入 AIStudio2API 现有的本地计数回退，并将结果返回客户端、保存到请求账本。数值属于本地估算，不是 Google 计费用量。此前关于上游缺少原生计数合同的结论不变。

## 计算方式

- 复用 `internal/aistudio/tokenizer.go` 内嵌的 `gemma3.model.gz` 和同一个 SentencePiece 实例，通过 `EstimatedTextTokens` 调用，不复制模型，不使用字符数除以常数。
- 输入按已校验、规范化的完整 user/model 历史计数，每个 content 增加 1 token，与主项目回退规则一致。已忽略、没有发给上游的 system/tool 选项和生成参数不计入。
- 输出累计可见文本、代码和代码执行结果，已返回的思考文本单独计入 `thoughtsTokenCount`。相邻文本合并后再分词，流式分块边界不改变结果，签名和引用 JSON 不计入正文。
- 附件仅能计算已解析的文件名等引用元数据，不能计算图片、PDF 等内容，不对 base64 分词。输入附件、输出二进制媒体或 Google Search 的内部工具上下文使 `incomplete=true`。建议数据属于网页元数据，不作为生成媒体计数。
- 未返回的缓存命中、隐藏思考和上游内部工具开销保持未知，不添加 `cachedContentTokenCount` 等字段。`totalTokenCount` 是本地可计数的输入、正文和已返回思考之和。

## 客户端返回

`generateContent` 的响应包含 `usageMetadata`。`streamGenerateContent` 的 SSE 和 JSON 数组只在成功末帧包含该对象，累计输入/输出不会在每个 delta 重复。失败或截断的流不发出成功末帧及完整用量。

标准数值字段为 `promptTokenCount`、`candidatesTokenCount`、`thoughtsTokenCount`（有值时）和 `totalTokenCount`。同时附加 `countingSource="local_gemma3_sentencepiece"`、`estimated=true`，以及必要时的 `incomplete=true`。

响应头也包含 `X-AIStudio-Usage-Source: local_gemma3_sentencepiece` 和 `X-AIStudio-Usage-Estimated: true`，便于会忽略扩展 JSON 字段的 SDK/网关识别来源。Python `google-genai==2.32.0` 与 JavaScript `@google/genai==2.22.0` 已通过离线真实 handler 测试，能够读取标准用量数值，流式用量仅出现在末帧。

`countTokens` 仍返回 501，模型目录不宣称已经具备原生计数能力。本次没有把本地估算包装成原生 `countTokens`。

## 日志与账本

Business 公开请求接入现有访问日志入口，仅在真实进入上游发送后记录请求，保留 API Key 校验。记录的模型、实际租用账号和 `channel=business` 与生成请求关联。同一次客户端请求只生成一条完成记录。

日志中的 `usage.source`、`usage.incomplete` 保存来源；SQLite 请求账本与明细 API 保存 `usage_source`、`usage_incomplete`，CSV 在末尾导出同名列。输入、回复、已返回思考及总量与客户端一致，现有统计可通过 Business channel 筛选。旧账本只追加列，历史记录来源保持空值，不改写历史计数。

## 验证记录

- 新增的用量、分块不变性、多轮历史、忽略参数、附件不完整、思考/代码、失败流、资源预算、实际账号观察器和账本迁移测试通过。
- Core、Host 全量测试通过；API/App/账本相关定向测试通过，[执行记录](evidence/local-token-usage-20261023.json)包含具体用例。AI Studio 包在该定向选择下没有匹配用例，仅完成编译。
- 账本的可选浏览器/性能用例没有启用，按原用例条件跳过。
- Python/JavaScript 双 SDK 的基础、原生代码/引用、多轮聊天、媒体/文件场景通过，新增断言检查标准用量可读、流式只在末帧出现。SDK 测试为管道内的真实 handler 测试，没有网络或真实模型调用。
- Gemini 全量回归有两项图片校验测试失败：`TestGeminiHistoryValidatesRolesAndUnsupportedPartsBeforeFiles` 和 `TestGeminiOutputSuggestionsNativeUnionAndBudget`。它们要求拒绝 MIME 为 image/png、内容为 `{}` 的二进制数据；当前未修改的媒体合同允许受限二进制透传。本次不更改图片合同或删除这两项断言，继续列为未通过。

后续发布准备已将上述两个旧测试的拒绝样例改为 `application/x-unsupported`，与现有受限媒体透传合同一致。保留了未知 MIME、错误 JSON、大小限制和历史预检的拒绝断言，Gemini 全量及双 SDK 回归已通过。本轮 Core/API/App/浏览器登录/账本测试通过，Host 测试被 Windows Application Control 拦截，未执行；前端 167 项测试、lint 和构建通过。此前执行记录作为历史证据保留。

本次未部署、重启或修改远端账号，也未调用真实 Gemini 模型。已有的其他系统策略阻断记录不由此次定向测试替代。

## 真实调用尝试（2026-10-23）

后续用户要求真实调用后，已构建当前版本，在独立本地实例与账本中复用已有加密账号。首个 Gemini 标准 `generateContent` 请求返回 503，账号状态显示 Google 认证阶段 HTTP 401、`auth_required`，成功生成次数为 0。失败请求记入独立账本为 `failed`，未返回或写入成功生成的本地用量来源。

用户随后选择“先保留真实调用未验收”。等待凭据的测试实例已关闭，端口 2049 已释放；普通输出、SSE 末帧和成功用量落盘的真实验收继续未完成。具体状态见[真实调用记录](evidence/local-token-usage-live-20261023.json)。
