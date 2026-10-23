# 第四阶段：原生续聊与双语言 SDK

本阶段按第三阶段移交中的原生多轮/session、响应保真与双语言 SDK 范围推进。公开协议依据见 [协议核对](phase4-protocol-evidence.md)；本地最终执行结果见 [验证记录](validation.md)。三个 Codex 子代理分别承担协议/历史解析、JavaScript SDK、响应解析与会话测试，主窗口负责 Core、交付提交、集成和真实账号验收。

## 原生会话续接

首轮仍是普通 Gemini `generateContent` 或 `streamGenerateContent`，创建新的 Business session。调用方后续提交完整的 `user/model/.../user` 历史；只有本进程已完整交付且历史匹配的前缀可以续接，发送给 Google 的 query 只含末条新增 user 消息。不会重放前面的生成、把历史拼成提示词、偷偷切换账号或新建 session 恢复历史。

- 前缀同时绑定已认证模型 Key 的 principal、公开模型、实际模型选择、完整 Binding/请求选项和原账号/config/凭据版本。账号标签编辑保持状态；凭据替换、禁用、删除、Business 关闭或应用重启使旧会话失效。
- 一个前缀只能被消费一次。并发分叉、历史编辑、过期/缺失及独立会话产生同一可见前缀的歧义，返回 HTTP409 `FAILED_PRECONDITION`。开始新的单轮请求才会创建新会话。
- 上游成功后，需完整写出普通 JSON、JSON 数组流的闭合或 SSE 终态，才登记新前缀。错误、取消、写失败或迟到旧版本交付不会恢复续接资格。已有账号冷却和并发限制继续生效。
- 前缀元数据仅在内存中保留15分钟；全局最多128条，每个 principal 最多16条，满时淘汰最早完成的记录。不保存提示词、模型正文或附件字节副本；只保存摘要及原 session 的有界引用。状态接口新增不含 session 名称的创建/续接计数。
- 最多33条输入 content（17个 user 回合），角色严格交替，最后必须是 user。用户每轮最多256 parts，模型历史最多4096 parts；全文本与签名合计1 MiB，附件全历史合计50 MiB/32项，完整请求68 MiB。
- 普通相邻文本块可按 SDK 拼接形式匹配；thought、签名和 suggestions 保留 part 边界。合法 base64 的等价编码只在身份摘要中规范化，响应 wire 不改写签名。
- 先校验所有消息及参数，再解析历史文件的本 origin、主体、MIME 和摘要。只上传末条消息新增附件；旧 file IDs 仅在其原账号、原 session 中引用。历史文件仍必须在本地 broker 有效，不能删除后继续以相同 URI 恢复。

调用方需要保留服务返回的完整 `content.parts`，包括非文本辅助 part，续接时保持模型与配置。仅复制 `.text` 可能丢失原 part，因而无法匹配前缀。变更配置（包括被忽略的可选字段）也可能要求开始新会话。这是已完成前缀的续接合同，并不支持任意外部历史导入。

## 响应与 SDK

上游响应现在拒绝重复 JSON 键、无效 UTF-8、未配对 Unicode surrogate、null thought/signature 和超过64层嵌套。真实诊断发现上游存在空 content 帧，因此无内容的 groundedContent 作为元数据帧跳过，不输出 part，也不据此结束。8 MiB 流限制包含前后空白，不能把超限截断当成正常 EOF；仍需明确成功状态及完整流结束才输出 STOP。协议失败只返回固定原因枚举，不泄漏上游正文或字段值。

Python 固定 `google-genai==2.32.0`，JavaScript 固定 `@google/genai@2.22.0`，JS 的独立 package-lock 保存依赖完整性信息。[JS 夹具](../../scripts/business-addon/sdk-js/README.md) 使用真实 Gemini/files handler、合成 Generator 和管道通信，不访问 Google。覆盖模型列表、生成、SSE、文件、映射0/false、可选参数忽略、原 part 保真和错误。夹具不替代 Host/API 鉴权验收。

固定 JS SDK 对标准 SSE `data: {"error":...}` 不一定抛出异常，可能返回无 candidates 的空对象后结束。消费者必须同时检查成功终态和错误，不能把迭代结束当成生成成功；服务端不会为此补造 STOP。SDK 对 `temperature:null` 的省略、JavaScript Number 的精度限制也已独立记录。

## 继续保留的限制

未发现当前 widget 对 systemInstruction、自定义 function declarations/tool 回合、schema/thinking 等高级参数的可用映射；没有用拼接或模拟宣称原生支持。未映射的可选参数继续按用户要求忽略，HTTP200不代表参数效果。函数调用/响应内容仍拒绝。Enterprise grounding 与 Gemini groundingMetadata 不同形，尚未转换；原生 countTokens/usage 不以文件摄取计数代替。

Host/API 两项按用户要求保留未验收，不执行其测试程序。app 测试程序也被系统策略阻断。最终本地三轮文件续聊和 JavaScript SDK 真实调用通过；本轮7次生成尝试中，修复前3次失败记录与最终4次成功均已保留。详见 [阶段汇总](evidence/phase4-summary-20261022.json)及验证记录。没有部署远程服务、提交、推送或启动外部审核。
