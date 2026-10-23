## ais03 更新账号后原生计数实测：2026-10-23

[更新后账号证据](evidence/token-ais03-updated-account-20261023.json)来自线上 `0d8c00b` / 账号 epoch 2：认证与配置 HTTP 200，新建合成会话的两轮 Argon 生成及两次详情回读均 HTTP 200 / SUCCEEDED，没有发现所扫描的用量字段、未知数值或用量响应头。标准 `countTokens` 的 `contents`、`generateContentRequest` 两种输入均 501 / UNIMPLEMENTED；标准 SSE 为 200，13 帧 / 12 个文本分片 / STOP，全部帧无 usageMetadata 或估算响应头。总计 3 次短生成，不填估算值，不把文件摄取计数或 token 标识当原生用量；仍未核实可实现的原生合同，因此未新增推测性生产解析器。

三个公开 Discovery 版本再次下载仍为 revision `20261022`，无计数方法。Google 首页返回 302 且未跟随，未取得新前端资产；不能将早先 bundle 复核写成本轮下载。运行后账号与 Argon ready、auth_verified/model_verified 为 true；配置、账号 epoch 不变，未部署或重启。脱敏、候选字段和来源约束的离线探测检查通过；工作区另一本地估算实现不属于本轮原生验收，也未部署。此次未重跑整个 Host/API/SDK 或大上下文验收，不替代原系统策略阻断记录。详细范围见[复核说明](token-count-recheck.md#ais03-更新账号后复核)。

## 原生计数复核：2026-10-23 较早记录

[本轮复核](token-count-recheck.md)新增现用 Standalone 脚本、revision `20261022` 的三个公开 Discovery 版本及 biz origin 的公开 schema，仍未找到可实现的原生 countTokens 或生成用量合同。[脱敏证据](evidence/token-recheck-20261023.json)记录本次 Core 包通过、固定 Python SDK 的 10 项无网络检查通过；contract 测试被系统策略阻止执行，其他原阻断包继续未验收。旧 Cookie 认证 HTTP 401 后，使用用户明确授权的临时 Bearer 完成[一次真实上游检查](evidence/token-upstream-live-20261023.json)：配置、创建会话、Argon 生成及同会话回读均 HTTP 200，原始流 3 帧并 SUCCEEDED，生成与回读均没有所检查的 usage/count 字段。新增生成仅 1 次，本机结果回传中断单独记录，不重发模型。生产计数/用量行为不变，不填零或估算值；未部署、重启或修改生产账号 Cookie，项目级 live_verified 仍为 false。

## 定向适配：2026-10-23 较早记录

本轮重点核实高级参数、工具调用和原生计数，三个子代理并行研究/开发，主窗口集成及真实验收。机器记录见 [本轮汇总](evidence/advanced-summary-20261023.json)，协议与代码范围见 [定向说明](advanced-tools-count.md)。**最新源码的真实验收尚未完成**：thought引用修复构建被Windows Application Control事件3077阻止启动，当前本地服务已恢复上一允许版本，不把离线通过写成完整运行通过。

后续只读进度核对发现2048已无监听，原PID79692也已不存在；下文ready/PID是当次回退验收时的历史快照。

- 新增标准空 `googleSearch` / `google_search` 工具映射；同账号租约先只读核实Google供应商，再启用上游 `webGroundingSpec:{}`。普通请求无额外查询，供应商不符400，无重试/工具降级重放。带时间/类型限制、混合工具字段或非空共享toolConfig时整个搜索变体忽略，避免删掉限制后扩大检索；类型、重复、别名冲突保持校验。
- 当前账号只读配置实际确认 `WEB_GROUNDING_TYPE_GOOGLE_SEARCH`。公开widget还发现 `generationSpec.thinkingConfig.thinkingLevel` 新容器，但当前Argon没有可用level ID；当前revision8的唯一启用模型没有生成参数映射。采样/schema/system/thinking及客户端函数往返仍未启用，不把本地接受未映射字段当生效证明。详见 [高级参数证据](advanced-parameter-evidence.md)、[工具证据](tools-protocol-evidence.md)。
- [首次真实SDK](evidence/advanced-python-live-20261023.json)与[thought边界复测](evidence/advanced-python-live-thought-boundary-20261023.json)共 **4次生成均HTTP200**，普通结果/STOP、流式和完整Chat续接成功；各创建1个session、续接1次。两轮SSE分别9/8个文本片段、增量跨度1.672/1.828秒，均返回1个官方PEP8来源；因无可定位supports，严格验收均为false，原报告保留。12项非法工具/函数消息请求均本地400；2次SDK原生countTokens均501，usage未伪造。
- 只读同一合成session确认真正的延迟引用结构：正文、suggestions和metadata-only消息共享replyId；首次4条segment逐字节匹配正文。新增按真实Part区间、同ID身份及完整EOF关联的有界实现。第二轮又确认3条segment只匹配可见正文、与thought零匹配，因此排除thought文本的正文offset、完整保留thought/signature和global Part索引。公开widget的普通文本过滤规则一致；所有late segments仍必须明确text及UTF-8偏移精确匹配，不能用最近文本或子串猜。无身份/歧义仍source-only，代码/其他typed界限保持。两份[只读结构](evidence/advanced-grounding-readback-20261023.json)、[含thought读回](evidence/advanced-grounding-readback-thought-20261023.json)不保存正文、会话标识或凭据。
- 最终允许范围 **Core101＋contract3＝104项顶层测试通过、0失败**，[最终日志](evidence/advanced-core-contract-final-20261023.jsonl)。工具专项7项及replyId最终专项9项包含在Core中，不重复累计。固定Python SDK2.32.0的 **10项无网络MockTransport检查通过**，[SDK日志](evidence/advanced-sdk-late-grounding-checks-20261023.log)，覆盖晚引用、中文/emoji偏移、真实终帧及完整历史、非法索引和时序识别。最新Gemini解析与Host状态断言只做静态核对，未运行受阻包。
- 最终cmd/internal/辅助包vet通过，最后thought变更又做Core vet通过；Go构建通过，保留模块stat-cache写入被拒的非致命警告。新构建51,619,840字节，SHA256 `D86F60D4C7631266615ED4A610043598960CAF4C5894F5F34BE6A003AAB30E2A`，保留在 `runtime/business-local/aistudio2api-advanced-release.exe`。**系统事件3077阻断了启动，未改名、改路径、重编译变hash或修改系统策略重试**。最后[SDK报告](evidence/advanced-python-live-final-20261023.json)在baseline连接失败，0次生成；不算模型失败，也不算最新修复已验收。[阻断记录](evidence/advanced-release-blocked-20261023.json)。
- 为恢复本地可用性，将同一路径回退到已允许的上一版SHA256 `93C8CE4F48476453EBBC83C297DD06029A31B87B950EDEC819BBD9FF9738DF91`。现PID **79692**、`http://127.0.0.1:2048`，revision8/原1账号保留，资源及并发0，读取ready；[回退记录](evidence/advanced-rollback-20261023.json)、[最终状态](evidence/advanced-final-status-20261023.json)。此运行版本已包含搜索供应商门禁及初版replyId关联，但在同ID包含thought时仍仅来源。回退后仅做健康读取，未再调用模型；新进程认证/模型成功标志重置，不能用ready替代认证证据。
- Host/API/resources/app、Gemini/config/files/token-count所有原系统策略阻断/延期包继续未启动、未验收；最新应用启动新增为未验收。前端本轮无修改，历史154项不重跑或计入本轮。四接点/根README、20项合成漂移及文档链接/diff检查通过；58项原任务和项目级live_verified=false保持。未提交、推送、远程部署、外部审核或重复大token压测。

原生countTokens继续501，生成usage缺省，具体依据见 [计数核对](token-count-protocol-evidence.md)。当前阻断解除前，最新thought引用修复只能标为已实现且离线通过，不能签署严格搜索grounding或全部适配通过。以下是较早的历史记录。

## 合并收尾：2026-10-22 历史记录

按用户最新要求，一次整合剩余开发，不再逐阶段交接。三个子代理并行处理协议及配置导入、Core媒体、双SDK，主窗口负责共享合同、Gemini历史、界面、集成与实测。完整机器记录见 [收尾汇总](evidence/completion-summary-20261022.json)，原58项任务中29个历史未关闭项的处置见 [最终清单](completion-roadmap.md)，当前接口范围见 [能力矩阵](capabilities.md)。这不是全部58项或完整发布准入通过的声明。

- 新增有界媒体 `inlineData` 转发，保留实际 MIME、bytes、thought/signature；支持严格ProtoJSON两种字母表及可选padding，规范编码和解码大小共同计入4MiB输出预算。模型媒体历史不再占用32个user附件槽，仍受Part数、总解码大小及请求预算约束。Models.list的正整数pageSize超过1000时按官方合同截断。详情及不支持项见 [协议依据](completion-protocol-evidence.md)。
- 新增控制台模型配置导入预览、确认和单层撤销，只追加默认停用、未核实的映射；来源摘要、revision CAS、原控制台会话及最终保存大小共同校验。同名不覆盖，账号/密钥/全局资源设置不导入。界面保护未保存草稿，内容或版本变化使预览失效。M08只是模型配置子集，未宣称完整账号迁移或数据库恢复。
- 新版已重启于 `http://127.0.0.1:2048`，PID **57340**，51,593,728字节，SHA256 `512685F6E946C0E4C1CFB4D4484D59CC0605AE7B3804A2A62D42B4368F30ED31`。旧版备份 `runtime/business-local/aistudio2api-before-completion.exe`。原1个账号保留；结束时ready、认证及Argon成功有效、连续失败0、并发和资源占用0，见 [最终状态](evidence/completion-final-status-20261022.json)。
- [合成配置管理回归](evidence/completion-import-20261022.json) **9/9通过**、0次模型调用：预览无写、只增加禁用映射、来源及版本冲突拒绝、撤销恢复、重复撤销和账号文档拒绝。revision由6增到8，排除revision后的完整配置SHA256前后一致，测试映射已撤销。这是运行中应用的本地集成结果，不替代Host/config单元测试。
- [Python SDK](evidence/completion-argon-python-20261022.json)两轮通过：上传/查询/列出/按name下载的内容一致，文件随机标记及42正确；流式20事件、**18个文本片段**，另实际返回1个代码和1个执行结果Part；首文本21.344秒、增量跨度6.703秒、正常STOP。完整Chat历史续接正确回忆；创建1个session、续接1次，文件删除后GET404。
- [JavaScript SDK](evidence/completion-argon-js-20261022.json)两轮通过：同样完成文件生命周期和随机标记/42，流式15事件、**14个文本片段**、16行与结束标记；首文本15.993秒、增量跨度5.168秒、正常STOP。SDK完整历史保留、下一轮回忆正确，创建1个session、续接1次；文件清理和资源归零。
- 共 **4次真实生成全部成功，0自动重放**。两个SDK额外各1次固定非法 `generationConfig:[]` 请求均在本地400；模型目录list/get及大pageSize请求通过。没有新高级参数映射，接受未映射可选字段不代表其生效。本轮实际媒体、grounding、citation计数均为0，不把正文链接或工具自述当结构化证据；未重复10万token专项，原153363/6816仍是文件摄取计数。
- 实际可运行的最终Go范围：**7个包、131项顶层测试通过，0断言失败**（Core85、其他46）。[Core最终日志](evidence/completion-core-20261022.jsonl)、[其他包日志](evidence/completion-go-20261022.jsonl)。其他包命令退出1，因为files和token-count程序被系统策略阻止启动，不是全部包通过。
- **Host/API/resources/app继续未启动、未验收。** 本轮Gemini、config、files、token-count测试程序又被Windows Application Control拦截，全部保留未验收，没有改名、搬动或修改策略重试。新增Gemini媒体历史/双SDK合成合同、4项config和3项Host用例因此未执行；Go vet编译检查和真实文本回归不能替代媒体历史测试。[Gemini阻断](evidence/completion-gemini-blocked-20261022.log)、[config原始阻断](evidence/completion-config-blocked-20261022.json)保留。
- 前端 **154/154**、typecheck、lint、build通过；保留已有大分块提示。Go最终build及cmd/internal/辅助包vet通过，导入与SDK包最终变更再次vet通过；构建有模块stat-cache沙箱写入警告。四接点、根README摘要、20项合成漂移、文档链接与diff检查最终结果记录在机器汇总。
- 初次Core沙箱运行只有既有httptest socket权限失败，同一正常命令在当前用户环境执行85项通过，未绕过应用控制。前端格式化器改写旧内联事件表达式曾造成16项编译失败，改为命名函数后154项通过；初次Gemini新增代码的无用import已修正，随后程序被系统策略阻断。原始失败均保留，最终结果不隐藏过程错误。内部子代理只读复核发现的媒体槽位、草稿、版本与保存大小问题已修复；未调用外部审核。

当前可落实的开发已合并交付。上游缺少合同的原生计数、批量/缓存/Live、任意函数工具和高级参数，生成文件授权下载、任意历史导入/重启恢复，以及人工登录、Linux完整采集、双WAA、真实候选升级/完整回退和发布准入继续明确列为边界或外部验收。未提交、推送或远程部署；项目级 `live_verified=false` 与58项原始任务记录保持。

## 第六阶段：2026-10-22 历史记录

三个子代理并行完成剩余原生输出的协议核对、流式映射和官方双SDK合同，主窗口集成、重启并用已有账号回归。具体范围见 [第六阶段合同](phase6-native-metadata.md)，完整机器记录见 [phase6-summary-20261022.json](evidence/phase6-summary-20261022.json)。

- 企业文档来源修正为 `retrievedContext {uri,title}`，不再由HTTP URI推断Google Search来源；新增 `citationMetadata.citationSources` 的实际URI/license投影，省略无法可靠转换的偏移。成功终帧只有完整匹配此前全部Part前缀才裁剪；同一字节映射用于跨旧Part与新后缀的引用拆分，typed/thought/signature/inline边界保持。元数据只在上游成功及完整EOF后交付，预算与失败不提交规则保持。
- 本地新版已重启于 `http://127.0.0.1:2048`，PID **77796**，51,546,624字节，SHA256 `FF5C5456FD84AF5930EA9F8E795D19FAFC84354B5E8B3797FB703D06EB37AEBB`。旧程序备份为 `runtime/business-local/aistudio2api-before-phase6.exe`。现有1个账号及配置revision6保留；结束时ready、连续失败0、并发及资源占用0，见 [最终状态](evidence/phase6-final-status-20261022.json)。
- [Python实测](evidence/phase6-argon-python-20261022.json)两轮通过：TXT文件随机标记和100项数据合计正确；首轮真实返回1个代码Part、1个执行结果、**18个文本片段**，首文本21.125秒、增量跨度6.704秒，正常STOP且末帧包含真实Part。官方Chat自动续聊正确回忆文件内容；创建1个session、续接1次，文件删除后GET404。
- [JavaScript实测](evidence/phase6-argon-js-20261022.json)两轮通过：15个事件、**14个文本片段**，首文本8.548秒、增量跨度5.722秒，16行及结束标记完整、STOP正常；SDK保留所有Part，下一轮正确回忆随机标记和42。只创建1个session、续接1次，最终资源归零。
- 共 **4次真实生成全部成功，无自动重放**。本轮仍未返回结构化grounding或citation来源，即使提示要求官方来源，因此这两类转换及累计引用定位只记为离线已验证；不把正文中的链接或“已搜索”当作原生结构证据。不伪造usage，没有新增高级参数映射，没有重复10万token专项。
- 最终选定范围按Core独立完整回归与主窗口其他包回归合并：**11个测试包、203项顶层测试通过，0失败**，Core79项、Gemini52项。[Core日志](evidence/phase6-core-final-20261022.jsonl)、[其他包日志](evidence/phase6-go-final-20261022.jsonl)和[双SDK日志](evidence/phase6-sdk-final-20261022.log)已归档。两次最终Go命令均退出0，实际未启动任何被用户保留的测试包。
- 双SDK新增专项每种7次管道请求、6次合成生成，验证retrievedContext、跨Part UTF-8支持、URI/license无伪offset、混合代码/签名/辅助Part及Chat历史不回传candidate元数据；旧专项同步通过。首次Python夹具把任意inlineData误当成首个user附件探针而发生KeyError，已缩小夹具探测条件，最终全包通过；[初始记录](evidence/phase6-sdk-initial-20261022.log)保留。这不是模型或服务生成失败。
- **Host/API和此前系统策略阻断的resources/app测试继续未验收，本阶段不执行**，没有改名/移动测试程序或修改策略。真实浏览器采集opt-in也未重复；无前端变更，保留第三阶段历史152项前端验收；无C编译器，race未运行。
- cmd/internal及两个辅助包的`go vet`通过，构建通过（保留Go stat-cache沙箱写入警告）。四接点/根README摘要、20个合成漂移、文档链接和diff检查通过。内部子代理只读审查未发现高置信阻断缺陷，没有调用外部审核。

精确前缀不能证明的终帧保留整段，仍可能包含重复内容；不猜代码执行ID或把缺少thought/辅助Part的文本当完整快照。原生countTokens/usage、工具函数回合、systemInstruction及高级参数语义、任意外部历史导入等仍未开放。项目级 `live_verified=false`，原58项任务内容/顺序/依赖保持；未提交、推送或部署远程服务。

以下第五阶段及更早内容是历史记录；第六阶段说明优先描述当前能力与验收状态。

## 第五阶段：2026-10-22 历史记录

按用户确认的“剩余原生接口适配”范围，三个子代理完成协议/引用、响应/边界和双 SDK 并行开发，主窗口集成并重启实测。当前合同见 [第五阶段说明](phase5-native-output.md)，机器汇总见 [phase5-summary-20261022.json](evidence/phase5-summary-20261022.json)。

- 本地新版已启动于 `http://127.0.0.1:2048`，PID **56888**，SHA256 `B72F2E6DC77E73E71FF494C03848FA4E87E6DA757270385AEF3E0590E733A140`，51,523,072字节。现有1个账号和配置 revision6 保留，旧程序备份 `runtime/business-local/aistudio2api-before-phase5.exe`。结束时 ready、Argon 已实测、连续失败0、并发和预算占用0。启动脚本已返回ready；外层脚本误用残留退出码报错后，通过PID/路径/摘要/HTTP独立复核成功，没有重复重启。
- [Python SDK 实测](evidence/phase5-argon-python-20261022.json)通过：合成TXT上传、文件标记及100项数据合计正确；首轮收到真正的 **1个 executableCode、1个 codeExecutionResult、17个文本片段**，首文本28.750秒，增量跨度6.921秒，最终真实Part随STOP交付。官方Chat下一轮自动携带完整历史，正确回忆文件标记和合计。只新建1个session、续接1次；测试文件已删除并GET404。
- [JavaScript SDK 实测](evidence/phase5-argon-js-20261022.json)通过：首轮17个事件、14个文本片段，首文本23.694秒、增量跨度5.547秒、最后真实Part及STOP；Chat保留全部parts，下一轮正确回忆随机标记和42。只新建1个session、续接1次；最终资源归零。
- 本阶段真实模型 **4次生成全部成功，没有自动重放**。两种SDK均未伪造usage。实际观察到代码与执行结果，**未观察到原生grounding元数据**，包括要求官方来源的续聊；引用转换仅记为离线已验证，不以文本链接代替原生引用证据。
- 最终Go回归 **11个测试包、188项顶层测试通过，0断言失败**，其中Core68项、Gemini48项。固定双SDK新增专项每种13次管道请求、11次合成生成，覆盖代码/结果/引用、Chat流式续聊、篡改历史409和失败前缀409；旧SDK专项也通过。[最终Go日志](evidence/phase5-go-final-20261022.jsonl)和[SDK日志](evidence/phase5-sdk-final-20261022.log)已归档。
- 整条Go命令退出1：`resources.test.exe` 被 Windows Application Control 阻止执行，**新增保留为未验收**；不是断言失败。初轮完整Gemini程序也曾被系统策略阻止，最终变更后的完整Gemini运行通过。[初轮阻断](evidence/phase5-gemini-initial-blocked-20261022.jsonl)保留。没有更名/移动测试程序、修改系统策略或绕过阻断。
- **Host/API 两项按用户要求未执行，继续未验收**；历史被阻断的app测试未重跑。浏览器真实采集是opt-in，本阶段未重复，第三阶段证据保留。没有C编译器，race未运行；没有第五阶段前端改动，未重复历史152项前端验收。
- `go vet`覆盖cmd/internal及两个辅助包通过，最终构建通过；仅有Go模块stat-cache沙箱写入警告。四外围接点/根README摘要、20个合成漂移、文档链接及diff检查通过。初轮旧JS夹具的事件数假设与保留最后真实Part的新合同不符，已修正事件数断言并保留完整内容/STOP检查；新增多Part测试验证逐块只交付一次。初轮Python夹具受沙箱临时存储权限影响，正常用户执行通过；初始日志保留。

原生请求tools/system/schema/thinking等未核实字段继续忽略，提示词触发代码执行不证明tools参数已转发。引用只支持有限HTTP(S)来源及可精确定位的文本区间；晚到/累计元数据仅保留来源。函数回合、原生countTokens/usage、任意外部历史导入仍未开放；本轮未重复10万token专项。项目级 `live_verified=false`，58项原始任务ID/顺序/依赖保持；未提交、推送、远程部署或调用外部审核。

以下第四阶段及更早内容是历史记录；第五阶段说明优先描述当前能力与验收状态。

## 第四阶段：2026-10-22 历史记录

已完成本阶段的原生 session 续接、历史/文件边界、响应完整性和双语言 SDK 适配，三个子代理并行后由主窗口集成。范围见 [第四阶段说明](phase4-native-conversations.md)，完整机器记录见 [phase4-summary-20261022.json](evidence/phase4-summary-20261022.json)。任意外部历史导入和高级模型语义不因此开放。

- 本地最终应用已重启于 `http://127.0.0.1:2048`，PID **75144**；SHA256 `548DC4BF625DC2677944B23751E1FEEFA78382CE4860CCCF33A19A70FF96533F`，51,479,552 字节。原账号及配置 revision 6 保留，第三阶段二进制备份为 `runtime/business-local/aistudio2api-before-phase4.exe`。结束时 ready、账号认证/Argon 成功证据有效、连续失败0、并发及资源占用0。
- [真实三轮与文件续接](evidence/phase4-argon-conversation-final-20261022.json)通过：首轮以 Python SDK 上传 TXT 并通过原生 REST 生成确认，第二轮提交完整历史后正确回忆随机文件标记及合计42，第三轮继续同一 session 并流式生成。创建计数仅增加1，续接计数增加2；流中 **20 个文本片段**，首文本 **7.297 秒**，持续增量 **8.562 秒**，最终 STOP。已消费的旧前缀返回409且计数不变；文件删除后 GET404。三轮共45.203秒，没有把历史拼成新 prompt。
- [JavaScript SDK 真实调用](evidence/phase4-argon-js-final-20261022.json)通过：固定 `@google/genai@2.22.0` 经同一本地 Gemini 标准接口返回预期 JSON/42，96.584秒、STOP、未伪造 usage。SDK 提示响应另含 inlineData；续聊调用方需保存完整 parts，不能只保留 `.text`。Python 固定版本为 `google-genai==2.32.0`。
- 本轮共 **7 次真实生成尝试，最终4次成功，修复前3次失败**；没有自动重放。首个文件请求和首个 JS 请求均为流阶段502；随后固定诊断明确捕获 `stream_empty_content`。新增校验错误拒绝了上游空内容帧，已改为跳过无正文元数据，不输出 part，仍要求明确 SUCCEEDED 和完整流结束。没有记录空帧具体是缺失、null 还是空对象，也不把前两次无详细诊断的失败强行归为同一原因。失败、诊断和成功报告全部保留。
- 最终按各包最新执行合并，**11 个测试包、167 项顶层测试通过**；其中最终 Core54项、Gemini38项。不是一次全仓库全通过。初轮 JS 夹具错误假定 SDK 会抛出 SSE error，修正后 Gemini 全包通过；中间诊断补丁的超限分类回归也在最终 Core 前修复，未放宽测试。Python/JS 管道夹具分别有15/19次合成生成，不访问模型。
- JS SDK 2.22.0 会丢弃标准 SSE error 字段并正常结束迭代；夹具验证服务端实际错误、没有 STOP、没有重放，并提供 [显式结束检查示例](../../scripts/business-addon/sdk-js/stream-consumer.mjs)。迭代结束本身不能视为成功。
- **Host/API 两项按用户要求未执行、继续未验收**。另外 `internal/app` 测试程序被 Windows Application Control 阻止，未更名/移动测试程序或修改策略绕过。原始失败证据见 [初轮 Go 日志](evidence/phase4-go-initial-20261022.jsonl)，最终 [Core](evidence/phase4-core-final-20261022.jsonl) 和 [Gemini](evidence/phase4-gemini-final-20261022.jsonl) 通过记录单独归档。
- cmd/internal及两个辅助包 `go vet` 通过，最后变更的 Core 再次 vet 通过；最终构建成功，仅保留 Go 模块 stat-cache 沙箱写入警告。四接点/根 README 摘要、20个合成漂移及 diff 检查通过。第四阶段未改前端，第三阶段152项/typecheck/lint/build只作为历史证据，没有重跑；race仍因无C编译器未执行。

续聊仅限当前进程已完整交付、同主体/模型/选项/账号版本的精确前缀，最多17个user回合，缓存15分钟、全局128/每主体16。历史文件需仍有效；错误、关闭或重启不恢复旧前缀。未映射的 system/tools/schema/thinking 等可选参数继续忽略，HTTP200不证明其生效；函数回合、原生 countTokens/usage、grounding 转换仍未支持。本轮未重复超过10万token专项，先前153363/6816仍是文件摄取计数。项目级 `live_verified=false` 保持；未提交、推送或部署远程服务。

以下第三阶段及更早内容为历史记录；第四阶段记录优先描述当前能力和验收状态。

## 第三阶段：2026-10-22 历史记录

已实现被动模型观察目录及控制台远程采集，范围与限制见 [第三阶段说明](phase3-observation-collection.md)；机器可读结果见 [phase3-summary-20261022.json](evidence/phase3-summary-20261022.json)。以下历史记录中的“未实现远程采集”“未知可选参数全部400”等说法由本段及最新参数兼容策略更新。

- 原控制台会话绑定、跨会话拒绝、退出/超时取消、清理后 CAS 保存、默认停用、资源归还，以及观察冲突/过期/迟到范围绑定都有离线验证。观察只进入待确认目录，没有自动启用任何新模型。
- 本机 `camoufox 152.0.4-beta.29` 实测通过 headless 截图、鼠标/键盘切换、文字输入及专属 profile 清理。最终应用也实际打开 Business 登录页，PNG 210,871 字节，Tab 请求成功，取消后 profile 数与预算占用均为0。没有输入登录凭据或发起浏览器生成。
- 固定 Python SDK、Gemini 标准接口、账号/文件隔离均有回归记录。前端 152 项测试、typecheck/lint/build通过。最终 Go 执行有12个测试包、217项顶层测试通过，Host/API两个测试程序被 Windows Application Control 阻止启动。用户明确选择保留两项未验收，最后新增的关闭清理错误传递案例未执行；之前14包311项及后续4包160项通过记录按历史版本保留，不能冒充最终全通过。原始事件、固定审批文件哈希见机器汇总。没有更改系统策略或移动测试程序来绕过拦截。
- Argon 原生 SSE：24 个文本片段，首文本11.375秒，文本增量跨度8.75秒，正常 STOP；可选 safetySettings / 未映射 generationConfig 被接受并忽略。HTTP200不是参数效果验证。
- TXT 引用正确返回标记及42，删除回读404。PDF两次在生成首包前暂态502，服务未自动重放；检查后移除了生成链路固定20秒响应头期限，保留总体请求期限及认证/上传短期限，并增加迟首包/取消/不重放测试。修复后新的 PDF 验收正确返回标记及47、STOP、删除404。之前没有记录底层网络错误细节，因此不把20秒限制断言为两个502的唯一原因。
- 本轮共6次真实生成尝试：SSE、TXT、修复后的PDF及最后短请求4次成功，之前PDF两次失败保留原始记录。最终应用重启于 `http://127.0.0.1:2048`，PID78796；最后短请求9.516秒返回预期JSON并正常STOP。账号认证及Argon实测状态有效，并发与资源占用均为0、连续失败0、配置revision仍为6。所有调用仅使用已有 Argon 账号与合成数据，没有新增模型探测。

未完成的真实验收：人工 Google 登录/验证码及无桌面 Linux VPS 环境；模型目录真实观察目前仍为空。完整持久化指纹配置、原生多轮/system/tools/计数、JavaScript SDK属于后续未验收范围。缺少C编译器，未运行race。未提交、推送或部署远程服务。

## 第二阶段：2026-10-22 历史记录

错误分类、冷却退避、共享资源预算及控制台状态已完成，三个子代理并行开发后统一集成。[设计说明](phase2-errors-resources.md)与[机器可读汇总](evidence/phase2-summary-20261022.json)记录本轮范围。

- 本地应用已重启于 `http://127.0.0.1:2048`，PID **72436**，SHA256 `2A1880F9795F1547127713A8AE0EC0AAB843A7A3F2420DD9ED1788F0B62BF5CD`，51,300,352字节。保留已有账号，旧二进制备份为 `runtime/business-local/aistudio2api-before-phase2.exe`。
- [最终Go回归](evidence/phase2-go-tests-20261022.jsonl)：Business全部包、API、app及两个Business命令辅助包退出0，**13个有测试包、297项顶层测试通过**；contract/dispatch没有独立测试文件。固定Python SDK夹具和上一阶段未能执行的旧认证版本测试均通过。此范围不等于运行整个仓库所有包。
- `go vet`覆盖cmd/internal及两个Business辅助包，退出0。Web **149/149**测试、typecheck、lint、build通过。四外围接点、根README摘要、20个合成漂移及diff检查通过。未运行race：CGO关闭且没有C编译器。
- 初次沙箱运行受DPAPI/原子替换权限限制，单独Host文件测试启动曾受应用控制阻断。**最终集成后的标准测试已正常运行通过**；未移动、更名测试EXE或更改系统策略。最终构建成功，有一条沙箱Go模块stat-cache写入警告。
- [Argon流式及预算热更新](evidence/phase2-argon-stream-20261022.json)：一次生成，32个事件/30个文本事件，首事件8.25秒，文本增量跨度12.687秒，最终`STOP`。流中预算降到64 MiB继续完成，预算及配置恢复256 MiB。非法预算保存没有推进revision，非法JSON不处罚账号、不泄漏占用。
- [TXT/PDF引用实测](evidence/phase2-argon-files-20261022.json)：两次生成，隐藏标记及计算结果42/47正确，均`STOP`，删除后GET为404。共计本轮3次真实生成，没有故意触发真实配额或认证错误。
- [参数预检](evidence/phase2-preflight-20261022.json)：temperature/maxOutputTokens/topP/thinkingConfig仍明确本地400；未声称其上游语义受支持。错误分类和退避使用模拟HTTP/RPC/网络响应验证。
- [结束状态](evidence/phase2-final-status-20261022.json)：`ready`、认证有效、Argon成功证据存在、并发占用0、预算占用0、连续失败0。恢复后的配置revision为6。

本轮未重复前次超过10万token测试。多轮、工具、高级参数、原生计数等后续工作不因此完成，`live_verified=false`保持。以下为历史记录，第二阶段记录优先描述当前实现和实际执行结果。

## 第一阶段 H1–H5：2026-10-22 历史记录

稳定引擎、不可变模型路由快照、局部账号撤销、动态并发、逐模型状态和独立标签编辑已落地。具体规则见 [phase1-hot-update.md](phase1-hot-update.md)，机器可读汇总见 [phase1-summary-20261022.json](evidence/phase1-summary-20261022.json)。

- 本地新版已重启于 `http://127.0.0.1:2048`，PID 26576，二进制 SHA256 `3A1B9E66F7686863770D364850AAEE540EB43F6D41480CC16E84E5BE12A98F67`。原账号 Vault 保留，旧8000服务未操作。
- [真实热更新 SSE](evidence/phase1-hot-update-live-20261022.json)：一次 Argon 生成，61个事件/59个文本事件，首文本12.391秒、末文本41.907秒，42.953秒完整 `STOP`；标签、显示名、并发与绑定热更新未中断流。忙碌/空scope未误报，凭据epoch保持，原配置及标签恢复。配置revision由2递增至4。
- [文件实测](evidence/phase1-files-20261022.json)：TXT/PDF 的隐藏标记及数值42/47均正确，删除后GET为404；两次生成共30.515秒。
- [参数预检](evidence/phase1-preflight-20261022.json)：temperature/maxOutputTokens/topP/thinkingConfig的未验证映射仍在本地返回400，不向上游发送生成。这不是高级参数支持证明。
- Go初轮12个有测试包通过，共256项顶层测试；随后补充Suspend后的组合回归通过Business各包及parameter-probe，共127项。最终API/app/token-count测试EXE被Windows Application Control阻止；它们初轮通过不能冒充最后重编译已运行。
- 最后新增 `TestReconcileDuringAuthenticationCannotCommitAnOldVersionJWT` 的core测试EXE也被系统策略阻止，该新增案例未执行。此前完整core及并发组重复20轮通过；未移动或更名测试程序规避策略。
- `go vet -mod=readonly ./cmd/... ./internal/... ./scripts/business-addon/parameter-probe ./scripts/business-addon/token-count`通过。Web 141/141测试、typecheck、lint、build通过，仅有原有大分块提示。`-race`因CGO关闭且无C编译器未运行。
- 四外围接点门禁、20项合成漂移及README摘要检查通过。仍无全量Go测试全通过结论，后续阶段及全能力 `live_verified` 不因此完成。

以下为之前各阶段的历史执行记录，以上新增记录优先描述当前实现。

# R3 集成验证记录

分支 `dev-2`，2026-10-22。本地已有账号的 Gemini 4 Argon 定向实测已完成，应用保持运行。标准生成、TXT/PDF引用、SSE真实通过，高级参数转发后明确被Google拒绝。58项整体计划尚未全部完成，项目级 `live_verified=false` 不等同于本次定向测试失败；没有部署或外部审核。

## 追加：超过10万token长上下文与流式（2026-10-22 19:35，Asia/Shanghai）

用户明确要求输入超过100000 token、输出超过3000 token。本次仅一次生成，通过本地 `http://127.0.0.1:2048/v1beta/models/business-gemini-4-argon:streamGenerateContent?alt=sse` 完成；未重启应用或修改运行配置，未发送不受支持的 `maxOutputTokens`。请求正文用提示词要求足够长的完整报告。

| 检查 | 结果 |
|---|---|
| 输入 | **153363 token**，370092字节合成文本，2000条记录，实际JSON请求372164字节 |
| 输出 | **6816 token**，3452个英文词，24554字节 |
| token口径 | 对实际输入/输出UTF-8字节分别调用Google `widgetAddContextFile.tokenCount`；SHA256与生成正文及保存输出一致。**这是文件摄取计数，不是Argon生成计费用量或Gemini原生countTokens** |
| 长上下文读取 | 开头/四分之一/中间/四分之三/末尾的5处随机marker、record tag、记录号、score和latency全部正确 |
| 流式 | HTTP200 `text/event-stream`；259个SSE事件，含257文本块、1个辅助inlineData part及成功终态 |
| 到达时间 | 首文本11.422秒、末文本102.094秒；文本持续增量到达90.672秒，总生成104.047秒 |
| 完整性 | 正常`STOP`，0个流错误，最终完成标记存在；保存输出与SSE逐块重建完全一致 |

包括两次计数在内总耗时118.25秒。计数辅助程序只做认证、建会话及文本上传，在底层发送前拦截生成请求，报告 `generation_request_sent=false`；没有额外生成或生成重试。

响应表格对下划线做了合法Markdown转义，最初按原始字符串检查“同一表格行”导致验收脚本误报；保留原始capture-report，并按CommonMark标点转义解码复核同一份SSE，5行全部精确匹配。没有改写模型输出，token计数仍针对原始字节，未重新调用模型。新验收逻辑同时检验错误数值不会被接受。

证据：[长上下文报告](evidence/argon-long-context-20261022.json)、[输入计数](evidence/argon-long-context-input-count-20261022.json)、[输出计数](evidence/argon-long-context-output-count-20261022.json)。完整输入、输出和原始SSE位于 ignored `runtime/business-local/long-context-20261022-argon/`。复现入口为 `scripts/business-addon/long_context.py`；`verify_long_context.py`仅重验保存产物，不联网。辅助计数器的离线解析、字节保真、固定域名和零生成测试通过。

这证明本次合成负载下的长上下文与增量输出正常，不是上下文最大上限、所有长任务或权威模型计费token的保证。

## 最新：本地 Argon 实测完成（2026-10-22 19:24，Asia/Shanghai）

用户确认系统放行后启动成功；复用本机同版本 Camoufox 缓存，避免隔离实例重复下载。应用监听 `http://127.0.0.1:2048`，进程48152；已有一个Business账号导入加密vault，只启用 `business-gemini-4-argon`，实际路由 `gemini-4-argon`。原AI Studio自动启动关闭。

| 项目 | 实测结果 |
|---|---|
| 官方 Python SDK 2.32.0 / `generateContent` | 10.047秒；预期JSON字段和值完全一致；`STOP` |
| 原生 Files + TXT `fileData` | 上传、引用、上游新会话物化成功；读取附件隐藏标记 `ARGON-TXT-7Q4M9C` 并计算42；`STOP` |
| 原生 Files + PDF `fileData` | 单页PDF读取隐藏标记 `ARGON-PDF-8V2K6R` 并计算47；`STOP` |
| 文件删除 | 两个测试文件均删除成功，后续GET均404 |
| `streamGenerateContent?alt=sse` | 26个事件、24文本块；首事件6.765秒，末事件17.609秒，文本持续增量到达10.157秒；首尾标记和`STOP`完整 |
| 本地参数预检 | temperature/maxOutputTokens/topP/thinkingConfig均按具体字段返回精确HTTP400，未默默忽略 |
| 真实上游参数转发 | Core探测确实发送原始值 `temperature=0`、`topP=0.5`、`topK=1`、`maxOutputTokens=64`、`responseMimeType=application/json`、`thinkingConfig.includeThoughts=false`；Google HTTP400逐项报这些字段为Unknown name。生产映射未开启它们 |

共4次应用生成成功、2次上游参数探测被拒绝、4次本地预检拒绝。首轮诊断只解析对象而漏掉流式错误数组，修复诊断器并通过合成单测后复验一次，得到全部六个字段的明确拒绝；两个探测均未完成生成。参数结论是“转发保真、上游不接受这些字段”，不是高级参数语义生效。

脱敏证据：[local-argon-live-20261022.json](evidence/local-argon-live-20261022.json)。不伪造usage/token count/modelVersion。本轮未重跑10万token压力请求，也未验证其他模型、多轮、工具或其他附件MIME。应用运行构建摘要与上一轮一致，未改动业务源码；新增启动脚本复用浏览器缓存、诊断器识别错误数组。

## 本地实测准备续项（2026-10-22 19:13）

用户已授权启动本地应用、复用已有 Business 账号并调用 Gemini 4 Argon，范围为标准 Gemini、文件引用、参数转发和流式验收。新构建成功，但启动被 Windows Code Integrity **3077/3033** 阻止（企业签名/应用控制策略）。用户回复会处理放行后通知；当前没有监听 2048、没有账号导入或本应用 Google 调用。未更改或绕过系统策略。

本轮修复 `role:model` 文本帧、累计终帧去重，并保留严格校验后的 `application/json+suggestions` 为原生 inlineData。普通及流式 SDK 能保持该 payload 的原始字节。启动与有界实测脚本已准备，见 [local-live.md](local-live.md)。

| 续项检查 | 实际结果 |
|---|---|
| 当前用户环境 `go test -mod=readonly -json ./internal/addons/business/... ./internal/api ./internal/app` | **退出0**，有测试的10个包全部通过；98项 Business、54项API、74项app顶层测试，config/browser/files/vault四包使用Go成功缓存。Host当前版本完整通过。两包无独立测试 |
| 固定 Python SDK 2.32.0 | 同命令内 `TestGeminiSDKOfflineHarness` 通过，11次合成生成，包含建议 inlineData 普通与流式解码；零网络socket |
| 首次受限沙箱组合回归 | 退出1；原子替换及DPAPI/持久化失败，正常Windows用户环境复跑上列命令通过。失败原始记录保留在 ignored runtime，未改测试绕过限制 |
| `go vet -mod=readonly ./internal/addons/business/... ./scripts/business-addon/parameter-probe` | 退出0 |
| 本地应用构建 | 退出0；产物 SHA256 `3E955E4AFEEF0A4D1F48C005ECCF68871526FB45EAFEF12C55FD7229C9451270`，66,788,352 bytes |
| 本地应用启动 | **失败，Windows应用控制阻止**；不是模型调用失败，尚未进行真实HTTP验收 |
| 四接点/漂移/diff | anchors通过、20项合成漂移通过、`git diff --check`通过；根README摘要保持 |

本段为放行前准备记录，由上方最新实测结果更新；旧全量 cmd/internal 系统阻断记录仍保留，不宣称本轮跑过全部旧包。

机器可读汇总：[integration-summary-20261022.json](evidence/integration-summary-20261022.json)。保留主回归原始事件：[go-final-20261022.jsonl](evidence/go-final-20261022.jsonl)，修复后回归：[go-followup-20261022.jsonl](evidence/go-followup-20261022.jsonl)。文档子窗口的较早门禁记录：[offline-docs-20261022.json](evidence/offline-docs-20261022.json)。

## 当前实际执行结果

| 检查 | 实际结果与范围 |
|---|---|
| `CGO_ENABLED=0 go test -json -mod=readonly -p 1 ./cmd/... ./internal/...` | **退出1**。Business所有有测试的包、API及大多数旧包通过；原aistudio/requestdb测试EXE被Windows Application Control阻止。app发现关闭后状态查询兼容断言失败，已修复并单独复跑通过，不能把原命令改记为成功 |
| Business主回归 | browser 22、config 4、core 9、fileapi 13、files 11、gemini 15、host 15、vault 2项顶层测试通过（共91，另有子用例）。contract/dispatch无独立测试；分流由API测试覆盖 |
| 最后状态兼容修复后 `go test -json -mod=readonly -p 1 ./internal/addons/business/host ./internal/app` | **退出1**。app完整回归通过，含关闭后状态=closed；Host单包测试EXE再次被Windows策略阻止。因此最新Host单包完整执行仍未证明，不能把主回归前的通过冒充最后复跑成功 |
| 固定 `google-genai==2.32.0` | 主回归的 `TestGeminiSDKOfflineHarness` 通过，9次合成Generate；真实SDK模型分页/get、原生生成、SSE/JSON数组、附件、文件续传/get/list/download/delete、未知字段与签名契约，使用MockTransport+管道，零socket |
| API/Host组合 | 模型Key/同consoleAuth、跨站/回环、登出取消、原Request/Body/Writer一次委托、旧alias/管理/上传优先通过；本地原生上传→fileData→假Google上传→生成、重启归属、不同Key隔离通过 |
| 浏览器采集组合 | fake driver及Host：严格Firefox152、URL/Cookie一致快照、取消/关闭、保存前清理、CAS冲突、采集默认停用和清理失败不保存通过。未执行真实OS浏览器进程 |
| 文件资源边界 | 存储/HTTP共24项顶层测试通过，含安全上传名而保留公开显示名、50MiB/TTL/配额、慢上传释放元数据锁、读写deadline取消复位、共享2个下载槽、在途删除及重启恢复 |
| `CGO_ENABLED=0 go vet ./cmd/... ./internal/...` | 退出0，覆盖模块最终代码（末次只读关闭状态分支调整由app测试/构建编译覆盖） |
| Web tests/typecheck/lint/build | 前端子窗口报告132项全通过，含16项Business测试；typecheck/lint/build通过。构建只有大分块提示。前端之后没有再修改 |
| Windows `go build -mod=readonly ... ./cmd/aistudio2api` | 退出0，生成66,768,896字节验证产物，SHA256与路径见汇总。未启动、替换或部署。沙箱有Go模块stat cache写入权限提示；最初输出到旧窗口目录失败，改为当前允许目录后成功 |
| `python -B -X utf8 scripts/business-addon/check_anchors.py` | 退出0；四接点精确恢复、其余旧文件/新增路径预算、根README指定摘要保持 |
| `python -B -X utf8 scripts/business-addon/check_drift.py` | 20个合成漂移用例符合预期；58任务文档/路径/依赖检查通过；不代表真实更新候选兼容 |
| `git diff --check` | 退出0 |

SDK的provider头须放在Client级`HttpOptions.headers`。2.32.0会覆盖每次`UploadFileConfig`中的headers；不能依赖后续续传一直携带自定义头，上传能力本身需要验证。

主窗口修复了清理错误优先级、关闭等待凭据提交、预读体生成并发槽、每新账号/session重传附件、持久文件owner，以及关闭状态查询兼容问题。没有通过移动/更名测试EXE绕过Windows策略。

## 明确未验证或未完成

- Windows策略阻止了aistudio/requestdb全量执行，以及最后Host单包复跑；没有全量Go测试通过结论。
- `-race`因`CGO_ENABLED=0`且无gcc/clang/cl未执行。根`go vet ./...`的旧runtime/review临时测试错误未修复；本轮只执行cmd/internal vet。
- JavaScript SDK未验收；Python SDK不替代它。SDK夹具不连接Google，Host/Core真实效果仍需另行授权。
- 远程/无头采集、自动modelId观察、browser generation transport、多轮历史/恢复、输出媒体底层客户端、M08导入预览未完成。高级参数仅按管理员逐字段证据门禁，不代表上游语义已核实。
- 未选择或测试真实upstream更新候选；只执行20个内存合成漂移。未提交、推送、启动验证产物或部署。真实登录/Business模型/双WAA/发布准入保持未执行。

## 历史基础阶段记录

以下从原 validation 保留，属于 Core/Vault/文件/采集后续实现之前的记录；本轮文档窗口未复跑。

| 历史检查 | 原记录 |
|---|---|
| `go test -mod=readonly ./cmd/... ./internal/...` | 通过 |
| `go test -mod=readonly ./internal/addons/business/... ./internal/api ./internal/app -count=1` | 基础接入通过 |
| 配置并发写入，12 个相同 revision 仅一个成功 | 通过 |
| Web tests | 120/120，含基础新增 4 项 |
| Web typecheck/lint/build | 通过 |
| `go vet ./cmd/... ./internal/...` | 通过 |
| Windows 验证构建 | 退出 0 且产物存在，未启动；有模块 stat cache 权限提示 |
| 原边界脚本 / diff 空白检查 | 通过 |
| 根 `go vet ./...` | 既有 review 临时测试未定义 `NewHandler`，未通过 |

原记录还提到初次受限环境的测试 EXE、临时文件替换和回环限制，以及正常执行授权后的通过结果。这不保证当前 Windows 策略允许新测试执行。历史行为测试覆盖原正文/Request/Writer、委托次数、管理/别名优先、共享鉴权与登出取消、跨站和回环限制、续传优先、缺省无写、配置损坏和冲突。

## 较早阶段授权记录（由上方续项更新）

较早阶段真实 Google 登录、Business 模型调用、双 WAA live、真实参数效果验证、部署、外部模型审核均未授权且未执行。本次用户已授权本地已有账号的 Argon 调用和参数验证，等待系统允许应用启动；双 WAA、部署和外部审核仍无授权。`business-gemini-pool` 等参考仓库的 live 报告不计入 `dev-2`。没有选择真实更新候选、提交或推送；完整发布准入 I04 仍未满足。
