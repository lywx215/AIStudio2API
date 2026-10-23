# 第六阶段：原生来源信息与累计流

用户授权继续完善剩余原生接口，三个子代理并行核对协议、实现流解析与引用映射、验证官方双 SDK；主窗口实现共享响应合同和 citation、集成、重启与已有账号回归。正式协议依据见 [第六阶段协议记录](phase6-protocol-evidence.md)。本阶段不更换上游或认证方式。

## 文档来源类型

第五阶段把具有HTTP(S) URI的 `documentMetadata` 投影成了 `web`。URI本身不能证明Google Search来源，第六阶段修正为 `groundingChunks[].retrievedContext {uri,title}`，保留来源顺序、去重及索引重写。这个兼容投影不表示实现了Gemini File Search请求工具；也不补造文档名、存储标识、检索正文或搜索查询。

共享响应合同仍能表达明确的web来源，但每个chunk只能有一个来源类型，Business collector产生retrievedContext。URI最多8192字节，标题最多4096字节；只投影有效HTTP(S)、无userinfo的URI，不获取链接。整个grounding仍最多128来源、512个支持区间、512KiB编码数据。

## 累计终帧与引用位置

解析器保留最多4096个实际输出Part的有界账本。成功终帧只有完整重述此前全部Part前缀时才裁剪重复部分；相邻普通文本允许重新分片，代码、结果、inlineData、thought和签名必须逐项精确一致，签名按实际wire值比较。只匹配一部分、缺少先前的思考/辅助Part、字段变化或类型顺序变化时保留整个终帧，不在中间搜索相同文本/代码来猜测去重。

前缀匹配和追加使用同一组字节映射：每个原始reply文本区间对应实际交付的全局Part索引及其局部偏移。终帧中的引用可以落到旧Part或新增后缀；跨Part的引用拆为多个完整覆盖的支持区间，逐段验证UTF-8边界、原文和来源索引。拆分后继续受512项/512KiB限制。映射有缺口、重叠、越界或原文不符时明确失败，不输出局部看似正确的引用。

不能裁剪的终帧会实际追加原文，因此同reply引用可绑定新Part；只有元数据、无内容或thought仍不猜锚点，只保留来源。groundingMetadata只在上游成功且完整EOF后发送一次，索引对应本次全部实际交付parts。

这是严格的累计帧兼容规则，不表示所有widget终帧都一定累计；不满足规则的重复内容会保留，避免误删。流开始后的错误、截断及终帧写失败仍不补造成功，也不登记可续聊前缀。

## Citation来源信息

新增 `candidate.citationMetadata.citationSources[{uri,license?}]`，仅投影上游 `groundedContent.citationMetadata.citations` 实际提供的HTTP(S) URI及license。去重身份包括URI和license，不为相同URI合并不同许可证，不补许可证、title或publicationDate。

Enterprise Citation的偏移没有可通用转换到Gemini多Part响应的证据，因此不输出startIndex/endIndex；Gemini允许省略这两个字段。它与grounding支持区间独立，不代表某个文本范围或置信度。固定Python和JavaScript SDK把wire的citationSources转换成客户端`.citations`，服务仍按原生wire字段发送。

最多128个citation来源，URI8192字节、license4096字节，原始及累计编码数据最多128KiB；JSON转义膨胀计入预算。citation与grounding、正文共同计入4MiB总输出限制。严格检查JSON、UTF-8、重复字段和字段类型；含用户信息/不适用scheme的URI不投影，不读取其内容。最终成功时只发送一次，后续错误不能释放成功metadata。

## 验证入口与剩余边界

Core覆盖完整typed前缀、失配回退、跨旧Part/新后缀映射、引用拆分预算及source-only citation。双SDK夹具包含thought/signature、UTF-8正文、代码、执行结果、suggestions等Part，验证普通/流式响应、两种来源metadata和Chat完整历史；候选metadata不作为content历史回传。

已有账号回归复用受限的 `phase5_live.py` 和 `sdk-js/phase5-live.mjs`，传入 `--phase 6`；每个脚本最多2次生成、无自动重试，固定本地origin且禁止授权重定向。Python同时测试合成TXT读取和清理。receipt增加retrieved/web/citation计数，零计数表示未观察到；文本自称“已搜索”不算原生引用证据。

最终结果以 [验证记录](validation.md) 为准。Host/API、resources和历史app阻断继续保留未验收，不执行这些测试包，也不改名/搬动测试程序绕过策略。原生countTokens/usage、自定义function/tool回合、systemInstruction和高级参数语义、任意外部历史导入及完整媒体输出仍未支持或未核实；未映射可选参数继续按既有用户要求忽略。
