# 第五阶段输出协议核对与建议

核对日期：2026-10-22。本文是只读调查和实施建议，不表示第五阶段功能、SDK 或真实账号验收完成。没有读取凭据、访问登录会话、调用模型、运行 Host/API 测试或修改源码。第五阶段细则尚待主窗口确定。

## 来源与边界

沿用 [第四阶段协议证据](phase4-protocol-evidence.md) 与 [固定来源清单](references/source-manifest.json)。清单只固定研究来源版本，并没有第五阶段独立合同。另读取本机 `C:/Users/lywx2/Projects/Gemini-Enterprise-Manager/server/enterprise.ts` 和 `output.ts` 作为工程线索；它们使用不同的公开 OAuth 上游，不是本 Business widget 的运行证据，也未将本机副本等同于清单中的归档版本。

本日重新核对的官方协议：

- [Enterprise AssistAnswer](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions.assistAnswers)：AssistantGroundedContent、AssistantContent、ExecutableCode、CodeExecutionResult、TextGroundingMetadata、Segment、Reference、SearchEntryPoint、CitationMetadata。
- [Gemini generateContent 响应](https://ai.google.dev/api/generate-content)：Part、ExecutableCode、CodeExecutionResult、GroundingMetadata、GroundingSupport、Segment、CitationSource。
- [Enterprise streamAssist](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist)：公开响应入口；未找到 widget 元数据增量/累计模式及跨帧文本归属的保证。

公开 Enterprise 合同对应 `discoveryengine.googleapis.com`；本项目用 `biz-discoveryengine.googleapis.com/.../widgetStreamAssist`。字段相似允许制定严格转换规则，不能证明此路径会产生这些字段或接受对应 tools 设置。

## 字段映射依据

| Enterprise 输出 | Gemini 输出 | 结论 |
|---|---|---|
| `executableCode.code` | `executableCode.code`，`language: "PYTHON"` | Enterprise 明确仅 Python，因而补充语言有协议依据。没有来源 `id` 时不补造。 |
| `codeExecutionResult.outcome/output` | 同名字段 | 成功、失败、超时枚举同名；未知/未指定状态不猜成功。输出缺省与空字符串需要明确处理。 |
| `segments[].referenceIndices` | `groundingSupports[].groundingChunkIndices` | 必须先完成来源数组的映射，再改写索引。 |
| `segments[].startIndex/endIndex/text` | `segment.partIndex/startIndex/endIndex/text` | 来源是 UTF-8 字节区间，终点不含；目标增加 Part 定位，区间相对该 Part。 |
| `references[].documentMetadata` | 可投影到来源已确定的 `groundingChunks` | URI/title 可保留；不能把任意企业文档都声明成 Google Search 结果。 |

Enterprise 单个 `groundingScore` 不等同 Gemini 每来源的 `confidenceScores[]`；不复制成重复分数。`searchEntryPoints[]` 是多项 HTML，Gemini 是单个 `searchEntryPoint`，没有通用无损折叠规则。`visualSegments`、引用文档页码/坐标、通用 chunkMetadata、独立 citationMetadata 也不能整体改名。特别是 CitationSource 没有 partIndex，不能从多 Part 的局部引用猜全局偏移。

## 建议先实现的代码 Part 范围

这是工程建议，不是额外授权：只接收上游原生 code/result union 并保留顺序、thought/signature 边界；不把 Markdown 代码块推断为 executableCode，不在本机执行代码，也不因收到结果而声称 tools.codeExecution 请求配置已支持。

建议输入校验与已有严格 JSON 策略一致：一个 Part 只含一个内容值，禁止 null、错误类型、重复键、无效 Unicode；只接受合同中的 Python 和结果状态。新字段必须计入既有输出字节/Part 上限，历史 code/result 必须计入文本预算。对于未观察到的扩展字段，保留明确未支持错误比静默丢字段可靠。

实现需同时覆盖 `contract.Part.MarshalJSON`、响应 DTO、`ModelHistoryDigest`、模型历史解析和预算。历史 code/result 只用于本进程已交付前缀匹配；user 中的工具消息与任意外部历史仍不因此开放。

当前 `core/stream.go` 的普通文本判断及 `terminalDelta` 主要排除 InlineData/thought/signature。新增 union 后应采用明确的普通 text 判断；否则空 Text 的 code/result 可能在终帧前缀去重时被删掉。代码和执行结果不能作为文本累计前缀的一部分，也不能从执行结果失败推导整个生成必然失败：需等待生成自身终态。

## Grounding 的可实现范围与累积规则

Gemini 官方文档明确：流中 `groundingChunks` 只包含此前未发送的新来源；`groundingChunkIndices` 按所有响应累计列表定位，顺序由客户端保持。建议首版在成功完成后一次发送完整 metadata，这满足首次发送全部来源的条件，也避开反复发送累计表导致 SDK 索引错位。若以后增量发送，需要稳定来源 ID 映射、只输出新增 chunks/supports，并保留后续支持对早先来源的引用。

建议转换器保存显式定位记录：每个上游 grounded content 的文本、对应下游 partIndex、该文本在目标 Part 中的字节起点，以及终帧去重映射。不能用全回答字符长度代替，也不能仅绑定到“最近一个文本”。

- 先解析 int64 字符串索引并检查非负、上限、`start < end`；不要经 float64 或 JavaScript Number 舍入后再验。目标请求/响应预算已经远低于安全整数上限。
- 检查两个端点都落在 UTF-8 字符边界，且切片与来源 text（存在时）逐字节一致；Go 字符串长度可作字节长度，JavaScript string.length 不可。建议夹具含 `中🙂A`、组合字符及边界截断。
- 相邻文本若合并为一个 Part，目标区间加已合并前缀的字节长度。中间出现 thought、签名、inlineData、code/result 时保留 Part 边界；不能把全部文本连接后仍写 partIndex 0。
- 终帧可能重复完整普通文本。必须将引用映射到真正交付的旧 Part/新后缀；若一个区间跨越多个交付 Part，只有在完整定位关系可证明时才可分成多个支持区间。直接删去重复文本再沿用原索引会错位。
- 来源数组过滤、去重必须同步重写 referenceIndices；不能保留悬空索引。URI、标题、metadata 项数和累计字节均需有界。不访问来源 URL，不执行 renderedContent HTML。
- metadata-only、thought 关联元数据、缺 content ID、累计内容与增量内容不明等情况，不猜测文本锚点。可以只保留可确认的来源，或明确暂不支持该元数据形态；不得伪造 citation 支持。选哪一种需要写入最终能力合同。
- 本次公开文档不足以确定 widget 的引用 source 类型。建议只开放经真实样本确认的子集；未确认的 web/retrievedContext 分类、搜索入口合并、visual/citation 转换保持未开放。

参考项目 `Output` 的来源去重和 UTF-8 校验有启发，但 `lastText/lastPart` 依赖上一事件，不能处理任意延迟元数据；其 `grounding()` 返回累计完整列表，也不能直接当作 Gemini 每帧增量发送。它过滤 thought 的行为与本项目保真合同不同，不能照搬。

## 建议验收清单

1. 合成 code/result：普通 JSON、SSE、数组流、只含代码、失败/超时执行结果随后生成成功、thought/signature、畸形 union、资源上限、终帧去重；确认没有本地执行或隐藏重试。
2. 历史续接：代码/结果完整回传可匹配，改变语言/代码/结果/边界不匹配；SDK 对代码 Part 不转为文本，不能因历史新类型绕开 owner/config/account epoch 隔离。
3. Grounding：多字节、多 Part、文本合并、代码隔断、终帧重复、跨 Part 区间、无内容元数据、重复/失效引用、非法整数/边界/文本、单次最终 metadata 和分片 SDK 解码。对未知形态确认明确边界，不能用测试中的自行假设宣称上游保证。
4. 真实调用只验证实际收到的字段，并记录 widget 来源、元数据形态、转换后定位和 SDK 终态；单纯 HTTP200 或模型回答“已执行/已搜索”不证明原生 code/grounding 被触发。未触发的功能继续标为真实未验收。

Host/API 两项继续按用户决定保留未验收；以上建议不要求运行它们，也不涉及外部审核、远程部署或其他线程授权。

## 确认范围后的 collector 实现记录

本节记录用户确认第五阶段剩余原生适配后新增的有限转换；前面的只读调查描述的是最初核对步骤。实现位于 `core/grounding.go`，不代表整个第五阶段已经验收。

- 只投影 `textGroundingMetadata.references[].documentMetadata` 中具有 host、无 userinfo 的 HTTP(S) URI；不获取 URI 内容，不处理其他引用类型。它是 URI 来源投影，不能据此声称 Google Search 工具配置或检索效果已支持。按精确 URI 稳定去重，第一次出现的标题保持不变。
- 只在调用方明确提供实际交付的同 reply 全文本与 Part 索引时生成 supports。区间使用 UTF-8 字节，支持 protobuf int64 十进制字符串及整数形式；缺省 startIndex 为0。拒绝越界、负数、溢出、非整数、截断字符、文本不匹配或悬空 referenceIndices，过滤不可投影来源时同步重写索引。
- 负 Part 索引表示无法可靠定位。该情况下只保留来源，不解释 segments，不绑定到上一段文本。当前集成约定将 thought、空内容、代码/结果及所有 terminal reply 作为无锚点处理；终帧累计裁剪的 supports 暂不开放。签名文本只有在同 reply 文本定位明确时才能保留对应支持。
- metadata 在最终成功时一次发送；不生成 confidenceScores、搜索查询、搜索入口、visual/citation 转换。相同来源及相同 Part/区间/来源组合重复出现时去重，不重复消耗保留项额度。
- 最多128个来源、512个支持项；单次原始 metadata 和最终编码 metadata 均限512 KiB，保留计量包含 JSON 转义膨胀与结构开销。上层仍需把最终 metadata 纳入4 MiB总输出预算。错误为固定安全消息；出现一次错误后不会再返回部分成功 metadata。
- 来源字段通过共享合同统一检查：URI最多8192字节，标题最多4096字节，均为有效UTF-8。HTTP(S) scheme大小写等价，但保留原URI字符串；拒绝前后空白、控制字符和userinfo。非HTTP(S)/userinfo来源仍不投影；可投影HTTP(S)来源的字段超限则在collector阶段固定502 `DATA_LOSS`，不会先接受、再到最终handler校验才失败。
- `nil`、`null` 和空对象可表示没有 metadata；有锚点的畸形结构、重复 JSON 键、无效 Unicode 不会静默转成成功引用。无可投影来源时不输出空 grounding 对象。

新增 collector 专项覆盖多字节/组合字符、稳定来源索引、多 Part、来源过滤、无锚点、空metadata、24种异常及配额边界，后续补充共享URI/标题的精确字节边界、大小写和无效字符检查。首次独立执行已完成编译，但沙箱临时 `core.test.exe` 被 Windows Application Control 阻止启动，因此该次不能记为测试通过；之后响应子代理的完整Core回归通过67项顶层测试，包含最初6项collector测试（`runtime/business-local/phase5-core-final-verified.jsonl`）。共享字段校验修改后，沙箱执行再次被策略阻止；同一 `go test -mod=readonly ./internal/addons/business/core -run '^TestGrounding' -count=1` 命令经正常当前用户执行权限运行成功，退出0、耗时4.453秒，覆盖最终7项collector专项。没有改动测试二进制路径、移动/改名文件或修改系统策略；该专项不替代最终整包回归。Host/API 未运行，真实 grounding 样本仍待已有账号回归实际观察。
