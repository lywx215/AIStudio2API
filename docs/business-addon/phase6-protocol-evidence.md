# 第六阶段协议核对与实施边界

核对日期：2026-10-22。本记录先以只读方式核对第五阶段源码、已固定的官方 SDK 类型和当前官方协议；建议不等同功能完成或真实 widget 验收。没有读取凭据、调用模型、更换上游或运行 Host/API/resources/app 测试包。

## 本阶段建议

1. 将 Enterprise `documentMetadata` 来源投影为 `retrievedContext {uri,title}`，纠正第五阶段仅凭 HTTP(S) URI 就标为 `web` 的分类。保持现有 URI/标题和总量限制，不复制引用正文，不补造文档资源名或 File Search 存储标识。
2. 为终帧精确去重建立文本字节定位映射。只有原始回复文本与已交付 Part/新增后缀逐字节对应时转换支持区间；跨 Part 区间拆分，无法证明归属时继续只有来源。
3. 可增加 `citationMetadata` 的有限来源级投影：仅 URI 和已有 license，省略无法可靠定位的 offset。它与 grounding 独立，不相互伪装。usage/countTokens 仍无可用映射依据。

以上不增加 OAuth、调用其他 Google 上游、推测生成参数，亦不把合成夹具当作 widget 真实字段证据。

## 文档来源类型

[Enterprise AssistAnswer 的 Reference/DocumentMetadata](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions.assistAnswers#Reference)描述引用文档及其 URI/title，并不以 URI scheme 区分 Google Search 与企业检索。HTTP URL 只能说明链接形态，不能证明搜索工具来源。

[Gemini GroundingChunk/RetrievedContext](https://ai.google.dev/api/generate-content#retrievedcontext)区分 web 与 retrievedContext，后者具有可选 uri/title/text。当前 Gemini REST 页面将其关联 File Search；本地固定 `@google/genai@2.22.0` 的 `GroundingChunkRetrievedContext` 又明确涵盖检索工具获得的用户数据源/公开数据集，字段包含 uri/title/text。共享 SDK 的 `documentName/ragChunk` 注明不支持 Gemini API，因此不能把 Enterprise 文档资源名塞入这些 Vertex 字段。

本阶段采取文档检索来源的兼容投影，不声称已接通 Gemini File Search 工具。仅保留实际存在的 URI/title，可避免复制 reference.content 时引入新的片段语义和内存上限；其余正文、pageIdentifier、chunkMetadata、文档名、fileSearchStore、mediaId 均不补造。仍不获取引用 URI。

实施建议：共享 contract 新增 retrievedContext union；Core 生成该类型；Gemini handler 检查每个 chunk 恰有一种来源。URI最多8192字节、title最多4096字节、有效UTF-8、HTTP(S)、无前后空白/userinfo/opaque；来源总128、支持总512、编码 metadata 512 KiB及总输出4 MiB保持。稳定去重和 referenceIndices 重写不能因输出字段改名失效。

## 终帧映射依据与算法边界

[Enterprise streamAssist](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist#response-body)明确 IN_PROGRESS 返回的是追加片段；[Reply.replyId](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions.assistAnswers#Reply)可标识跨帧属于同一最终回复的部分。这是公开 API 的身份线索，不能推断 widget 一定返回 replyId，也没有因此获得“所有终帧都是累计快照”的保证。

[Gemini Segment](https://ai.google.dev/api/generate-content#segment)使用目标 Part 索引和 Part 内字节区间。来源 TextGroundingMetadata 的 segment 同样以 UTF-8 字节定位，但定位原始 grounded content；终帧裁剪后必须重新映射，不能沿用原始偏移。

建议核心映射类型由流解析层提供：

```go
type streamTextSpan struct {
    SourceStart, SourceEnd int // 同一原始 reply.text 的字节区间
    PartIndex, PartStart   int // 实际交付 Part 及其字节起点
}
```

collector 接口为 `addProjected(raw json.RawMessage, text string, spans []streamTextSpan) error`。旧 `add(raw,text,partIndex)` 可构造单 span；nil/空 spans 表示只有来源。流解析层证明源与目标字节相等、原顺序、不重叠；collector 再验证 segment 范围、UTF-8 边界、文本及来源索引，按 span 交集拆分 supports。支持区间必须被 spans 完整覆盖；不能通过删掉未覆盖部分制造看似有效的引用。每次拆分后继续应用512支持/512 KiB上限，handler最终针对真实 parts 复验。

必须从现有已交付前缀做精确匹配，不能用任意子串查找、最长相似匹配或最近文本猜测。Part类型、thought及签名边界不因正文重复而消失；代码/结果本身不获得文本引用位置。任何 typed 累计前缀扩展都应使用完整已交付 Part身份，不能因文档说明 IN_PROGRESS append 就默认 terminal 重发结构。错误/截断/终帧写失败仍不得输出成功或登记续聊。

groundingMetadata 继续只在最终成功时一次发送。[Gemini GroundingSupport](https://ai.google.dev/api/generate-content#groundingsupport)规定流中引用索引针对累计来源，不能在每帧重复完整来源表。referenceIndices 必须在过滤及去重后稳定映射；不补 confidenceScores 或搜索查询。

## Citation 的严格有限子集

[Enterprise CitationMetadata/Citation](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions.assistAnswers#Citation)真实字段是 `citations[]`，各项含 uri/license、局部 startIndex/endIndex、title/publicationDate。[Gemini CitationSource](https://ai.google.dev/api/generate-content#citationsource)的 startIndex/endIndex/uri/license 均为可选；因此省略 offset 的来源级 URI/license 投影有直接合同支持。

推荐 wire 形状：

```json
{"citationMetadata":{"citationSources":[{"uri":"https://example.test/source","license":"upstream-value"}]}}
```

这里的 license 只在来源实际有值时保留；不生成许可证或推断代码归属。空、非HTTP(S)、含userinfo的 URI 不投影；源 title/publicationDate 没有对应目标字段，明确不映射。来源级列表不提供行内位置，也不能表示 grounding 置信度。

Enterprise Citation 索引说明不像 TextGroundingMetadata 明确标注 UTF-8 字节，Gemini CitationSource 又没有 partIndex。当前没有可靠的通用多 Part偏移合同，所以本阶段不复制 offset，即使数值看似落在文本范围也不认定正确。可先用有界来源去重列表完成普通/SSE/双SDK字段验收。

固定 JS SDK 对外类型是 `CitationMetadata.citations`，但 Gemini REST wire 为 `citationSources`；这是 SDK 转换命名区别，不能因此把服务 wire 改成 citations。应通过两个官方 SDK 的实际转换夹具检查 URI/license，没有 source offset 时不得自动得到0。

## 仍没有依据的能力

本次重新搜索 Enterprise AssistAnswer 与 streamAssist 合同，未发现 `usageMetadata` 或 `tokenCount`；`assistToken` 是请求/响应对的标识，不是 token 数。[Gemini UsageMetadata](https://ai.google.dev/api/generate-content#usagemetadata)区分 prompt、candidate、thought、tool-use及有效缓存输入，故文件摄取计数、字符数或本地 tokenizer 不能填入。保持 omission 与 countTokens 未实现。

当前 [GenerationSpec](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist#GenerationSpec)仍只有 modelId。没有新增采样、schema、thinking、自定义工具或 systemInstruction 的 widget 映射证据；可选参数策略继续沿用既有用户要求。

## 验收建议与当前状态

离线应覆盖来源类型和union、UTF-8及边界、精确前缀/分割/映射缺口、重复终帧及签名/代码边界、引用过滤重映射、拆分后预算、citation URI/license无offset，以及两个官方SDK的普通和流式解码。真实回归只报告实际观察到的来源类型/支持/citation字段；文本自称“已搜索”不算结构证据。

本记录创建时仅完成协议调查和接口计划；后续实现和执行结果由对应阶段合同及验证记录归档。Host/API及用户要求保留的系统策略阻断测试继续未验收，不执行host/api/resources/app测试包，不复用或改造旧临时可执行文件路径。

## 引用模块实施记录

主窗口确认上述范围及共享合同后，`core/grounding.go` 已将文档来源改为 `retrievedContext {uri,title}`，不再输出web标签，也没有复制片段text或补造存储资源名。稳定URI去重、过滤后索引重写及原限额保持。

已实现冻结接口 `addProjected(raw,text,spans)`：验证源文本UTF-8、source区间、Part索引/起点、顺序、重叠及整数范围；有来源的segment必须完整落在映射覆盖范围，跨Part拆成精确文本支持项。无spans仍只保留来源。旧add只构造单一文本映射，不按最近文本推测。拆分后的重复支持稳定去重，新增项按512支持/512 KiB计量；不完整映射在固定安全错误后不能导出部分metadata。

新增映射测试覆盖中文/emoji跨Part、非零目标Part起点、完整性缺口、source/target重叠或逆序、溢出、UTF-8字符截断、空映射、拆分后512项精确边界及JSON转义后的编码预算。修改后执行 `go test -mod=readonly ./internal/addons/business/core -run '^TestGrounding' -count=1` 成功，退出0、3.453秒，共10项Grounding顶层测试；这是collector专项，不替代最终整包、双SDK、重启及真实账号验收。其后仅更新本文档，collector源码保持上述测试状态。
