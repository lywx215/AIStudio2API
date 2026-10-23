# 第五阶段：原生输出和 SDK Chat 续聊

用户确认按剩余原生接口适配推进，三个 Codex 子代理并行负责协议与引用、响应解析与边界测试、双 SDK 合同。主窗口负责共享合同、Gemini 历史与响应、集成、重启和已有账号实测。本阶段不增加新的上游认证方式，Host/API 两项按用户要求继续保留未验收。

## 原生响应

`content.parts` 新增 `executableCode` 和 `codeExecutionResult`。Enterprise 明确返回的代码映射为 `language: PYTHON`；支持的结果为 `OUTCOME_OK`、`OUTCOME_FAILED`、`OUTCOME_DEADLINE_EXCEEDED`。缺省结果 output 保留为空字符串。严格检查类型和互斥字段，保留代码、结果、thought、thoughtSignature 和顺序，不附加伪造的 text/id，也不在本地执行模型代码。代码、结果和签名均计入4 MiB输出预算。

完整模型历史允许上述 Part，包括 camelCase/snake_case 别名，摘要包括其真实内容；修改代码、结果或签名不能续接已完成前缀。用户当前消息中的代码执行/函数 Part 仍不支持。相邻普通文本仍允许 SDK 合并，类型和签名边界不能被抹掉。

引用只转换上游实际返回的 `textGroundingMetadata`。HTTP(S) documentMetadata 来源稳定去重，移除不适用的来源类型；含 URI 用户信息的来源不反射给客户端。仅当同一回复的 segment 能精确对应本次实际交付的完整文本 Part 时，输出 UTF-8 字节区间和来源索引。元数据晚到、仅元数据帧、thought 或裁剪的累计终帧只保留来源，不猜支持区间。详见 [协议证据和字段边界](phase5-protocol-evidence.md)。

`groundingMetadata` 只随完整成功终帧发送一次，`partIndex` 对应本次响应按顺序累计的 `content.parts`。最多128来源、512支持区间、512 KiB编码元数据，并计入4 MiB总输出预算。不把 aggregate groundingScore 伪装成 confidenceScores，不补造 webSearchQueries、citationMetadata 或 usage。代码/结果流缺少可靠的累计去重证据，因此保留类型事件；含代码的终帧可能重复累计文本，调用方应保留原始 Part。

## 官方 Chat SDK

固定 Python `google-genai==2.32.0` 在收到 parts 为空的终帧时不能可靠登记流式 Chat 历史；固定 JavaScript `@google/genai@2.22.0` 将多个流块记成相邻 model content。本阶段针对两者进行了真实 SDK 的离线合同验证。

- 服务保留最后一个真实 Part，待上游完整成功结束后随 STOP 交付。每个 Part 只发送一次；最多增加一个上游 Part 的等待，不生成空占位或重复文本。只有一块的响应会等到成功后才发出。
- 上游失败时可交付已收到的最后内容，但不补造 STOP，不登记成功历史。终帧写出或 Flush 失败后不重发终帧。
- 解析相邻 model content 为一个有序模型回合；相邻 user 仍拒绝。归一化后最多33回合，每个模型回合合计4096 Parts；原始 content 片段最多4129。全文本、代码、结果及签名仍共用1 MiB历史预算。
- 延续第四阶段精确前缀、单次消费、账号/config/主体隔离合同。SDK Chat 必须完整保存 parts；只存 `.text` 会丢失辅助数据。groundingMetadata 属于 candidate，不属于续聊 content 身份。
- 固定 JS SDK 仍可能吞掉 SSE 错误；消费方必须确认 STOP。错误流前缀不得续接。

## 验证及范围

离线夹具使用真实 Gemini handler、完整代码/结果/引用以及合成 Generator；分别覆盖两个官方 SDK 的普通 Chat、流式 Chat→下一轮、所有结果枚举、代码历史篡改和失败前缀。它不替代真实账号或 Host/API 验收。

真实脚本只用合成文件和新会话，固定本地 origin、单次请求且无自动生成重试；报告只保留计数、布尔结果和固定错误类型。是否真的观察到代码结果或引用以实测报告为准。通过文本提示请求执行代码/搜索不证明原生 `tools` 已转发；未核实的工具、systemInstruction、schema/thinking 等可选参数策略不变。

最新测试、重启信息和真实结果见 [验证记录](validation.md)。原生 countTokens/usage、任意外部历史导入、自定义 function/tool 回合及完整媒体输出仍未支持。项目级58项计划不因此标记全部 live_verified。
