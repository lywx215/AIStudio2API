# 剩余原生协议的最终证据边界

核对日期：2026-10-22。此记录区分公开协议可证明的转换、本地实现状态和真实 widget 观察；公开 Enterprise OAuth 接口不是当前 Cookie/JWT widget 接口的调用授权或等价性证明。最终执行结果由 [validation.md](validation.md) 及主窗口汇总记录，原始58项任务的处置见 [completion-roadmap.md](completion-roadmap.md)。

## 可直接落实的剩余转换

Enterprise `AssistantContent.inlineData` 的 `Blob` 为 `mimeType` 与 base64 `data`；Gemini `Part.inlineData` 使用同一基础结构。因此可保留真实返回的媒体 bytes，使用有界 MIME 范围、严格 base64、互斥 part 和编码/解码共同预算；模型历史必须按 MIME 与 bytes 摘要校验，不能转换成文本。只接收返回值不证明 Argon 可以生成每一种媒体，也不证明 `responseModalities` 或图片/音频配置有效。参见 [Enterprise AssistantContent/Blob](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions.assistAnswers#AssistantContent) 和 [Gemini Part/Blob](https://ai.google.dev/api/generate-content#Part)。

该项已在本轮收尾源码中实施；范围和验证状态以 [能力矩阵](capabilities.md) 为准。已有 suggestions 的严格 JSON 检查继续保留；不解码或执行返回媒体，不从外部 URL 拉取数据，不把 `fileId` 当作公开下载链接。新增Gemini专项在本轮被Windows Application Control阻断，源码存在不能替代执行验收。

## replyId 与终帧

官方 `Reply.replyId` 只证明同一 AssistAnswer 中相同 ID 的消息属于同一个逻辑回复。`IN_PROGRESS` 的 reply fragments 应追加；它没有承诺相同 ID 的 bytes 是重传、覆盖或完整快照。因此 ID 可以作为有界身份信息，不能单独触发删除、覆盖或把媒体块拼接成一个文件。现有完整已交付 Part 前缀匹配及来源字节映射仍是累计终帧去重的依据。参见 [Reply](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions.assistAnswers#Reply) 和 [StreamAssistResponse](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist#response-body)。

没有可判定语义的字段应保留为未映射，不能把相同文本搜索结果、相同 URL 或相同 replyId 当作完整前缀证明。已有跨 Part grounding 的精确区间映射、sources-only citation 规则见 [第六阶段证据](phase6-protocol-evidence.md)。

## 文件与参数的停止边界

| 项目 | 证据与最终处置 |
|---|---|
| 上传上下文文件 | 公开 `addContextFile` 接受文件名、MIME 和 bytes，返回 session/fileId；现有 widget 上传加同账号/session 绑定已有真实 TXT/PDF 验证，不需另造第二套文件服务 |
| 原生 Files 外观 | 本地 broker 已实现上传、续传、list/get/delete/download 与主体校验；这是本地资源合同，不声称 Google 上游文件已同步删除 |
| 上游生成文件 `file:{mimeType,fileId}` | 字段存在，但不等于 Gemini `fileData.fileUri`。本轮未取得对应 widget 授权下载、资源期限和账号归属的完整证据，继续明确拒绝，禁止拼出猜测下载 URL |
| 文件 tokenCount | 官方已标记 deprecated，且不保证所有文件类型返回；历史长上下文辅助计数仍只是文件摄取计数，不能填成 Argon usage 或原生 countTokens |
| 采样、schema、thinking 参数 | `AssistGenerationConfig`/`GenerationSpec` 仅公开 modelId；已有六字段 widget HTTP400 证据不变。没有逐字段映射证据时按用户要求忽略可选参数，不宣称语义支持 |
| tools/system/functions | Enterprise 的工具注册及搜索/图像/视频开关不等于 Gemini 函数声明或 systemInstruction。不得从 UI 标签或成功文本回答推导请求字段映射；函数消息不丢弃后继续调用 |
| usage/countTokens/批量/缓存/Live/embedding | 当前 Business 管线无已验证合同；保持未实现或未映射。不能通过字符估算、文件摄取、后台重复生成或换上游伪造支持 |

文件依据：[Enterprise addContextFile](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions/addContextFile)、[Gemini Files](https://ai.google.dev/api/files)。参数依据：[AssistGenerationConfig](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/AssistGenerationConfig)、[GenerationSpec/ToolsSpec](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist)、[实际参数拒绝证据](evidence/local-argon-live-20261022.json)。

## 可完成与不可替代的验收

本地可完成有证据的媒体传递、SDK/资源边界、配置导入预览与安全撤销，以及文档对齐；不需要无限增加“下一阶段”。Host/API/resources/app 的系统策略阻断仍按用户要求保留未验收；人工 Google 登录、Linux 无桌面完整采集、双 WAA 在线共存、真实上游候选升级和完整回退需要相应环境或另一次明确验收。构建成功、离线夹具、协议可转换及本地 Argon 成功均不能替代这些条件。

本研究只读官方公开资料与仓库，不读取凭据、不调用模型、不运行受阻测试或外部审核。

## M08 本地模型配置导入子集

本轮新增 `config/import.go` 的纯逻辑预览和 `host/imports.go` 的管理接口。参考格式来自已只读核对的 `G:/code/gemini30/business-gemini-pool/model_routing.py`：`id/name/routing_mode/upstream_model_id/enabled`，模式 `explicit/auto/unconfigured`；该源码明示的旧格式 `id` 作为上游ID仅保留为未核实、禁用候选。未读取参考项目的账号文件。

输入只接受 `{"models":[…]}` 或 `{"bindings":[…]}`；其他顶层字段、账号文档、未知字段、重复键、非法UTF-8/代理项、超过32层或128KiB来源拒绝。新增绑定强制disabled/unverified，清空证据和账号scope，参数映射的verified/evidence也清空；同名跳过。全局启用、资源额度、超时、现有模型、账号、凭据和文件均不导入或修改。这是原M08的模型配置子集，不能宣称账号导入或完整迁移恢复完成。

| 管理接口 | 请求与返回 |
|---|---|
| `POST /api/addons/business/config/import/preview` | `{revision,source}` → `{revision,source_hash,added,skipped,warnings}`；纯预览不创建目录或写配置 |
| `POST /api/addons/business/config/import` | `{revision,source_hash,source}` → `{config,rollback_id}`；重新计算来源摘要与候选，revision CAS，只追加新绑定；零新增拒绝 |
| `POST /api/addons/business/config/import/rollback` | `{revision,rollback_id}` → 恢复后的Config；revision单调增加，只撤销最新一次且配置之后未保存 |

来源hash按紧凑JSON计算；变更内容或键顺序需重新预览。最终保存使用实际缩进格式及换行计算128KiB上限，包括revision增加的位数。撤销只在进程内保留一层快照，绑定原有效控制台会话；登录退出/过期、重启、后续配置保存或再次导入会使旧撤销失效。前端页面离开时清除receipt，不将撤销ID持久化到浏览器。

纯配置新增4项测试，首次正常执行 `go test -mod=readonly ./internal/addons/business/config -run '^TestImport' -count=1` 被Windows Application Control阻止启动 `config.test.exe`，无断言执行，之后未重试或移动文件；[原始命令、退出码与输出](../../runtime/business-local/completion-config-blocked.json)从已取得的工具结果原样保存。之后的会话绑定及最终序列化大小修正未执行配置单测。Host新增3项用例按用户要求未执行。gofmt与diff检查通过。

主窗口已在运行的本地应用完成[合成禁用模型管理验收](../../runtime/business-local/completion-import-live.json)，9/9检查通过：预览无写且结果disabled/unverified、来源hash变化拒绝、只追加禁用模型且其他设置不变、过期revision拒绝、撤销恢复、旧撤销与完整账号文档拒绝、最终设置摘要恢复。revision从6增加到8，设置内容SHA256前后一致，模型调用0次。该记录证明实际管理HTTP集成子集，不能替代未执行的Host单元测试或被阻断的config边界测试。
