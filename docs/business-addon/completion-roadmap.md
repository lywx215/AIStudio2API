# R3 剩余任务的最终处置

2026-10-22。原始 [tasks.json](tasks.json) 有58项，其中29项仍保留 `in_progress`、`live_pending`、`not_started` 或 `implemented_awaiting_verification`。这些历史标签不等于29项待重新开发；阶段1–6的追加证据优先说明现状。本表不修改原任务状态，不把未验收改成完成，也不安排无期限的后续阶段。

处置含义：**已有实现**仅需本轮允许范围回归和证据对齐；**本轮补齐**是实际新开发；**上游边界**无法凭本地模拟补齐；**外部验收**需要指定环境/授权/候选。阶段6最新基线为 [203项选定范围及4次Argon实测汇总](evidence/phase6-summary-20261022.json)，本轮结果尚须主窗口记录。

| 原任务 | 最终处置 | 证据与未完成条件 |
|---|---|---|
| T03 能力合同 | 已有实现＋本轮文档对齐 | 双SDK固定版本、文件和原生合同见 [能力矩阵](capabilities.md)；不再沿用“JS未验收”的早期说明 |
| B02 JWT缓存/epoch | 已有实现 | [阶段1](phase1-hot-update.md)、Core auth/credential测试与后续真实生成；不故意用真实账号制造认证失败 |
| B03 HTTP/session | 已有实现 | 固定HTTPS、禁跳转、超时取消、精确续接；[阶段4](phase4-native-conversations.md)及阶段6双SDK实测 |
| B06 调度/恢复 | 已有实现 | [阶段2](phase2-errors-resources.md)错误分类/退避/并发；不自动重放不确定提交、不换账号恢复旧session |
| B07 分层健康/计数 | 已有实现＋上游边界 | [阶段1状态](phase1-hot-update.md)区分配置、调度、认证和实际成功；countTokens仍无权威上游合同 |
| B08 ContextFile客户端 | 部分实现＋上游边界 | 输入上传及session绑定已实测；生成文件元信息/授权下载缺widget证据，不能把本地broker下载冒充上游客户端完成 |
| C02 观察器 | 已有实现＋外部验收 | [阶段3](phase3-observation-collection.md)有被动观察/待确认目录；真实模型目录为空，人工登录后的字段采集未完整验收 |
| C03 人工接管 | 已有实现＋外部验收 | 阶段3本机远程无头截图、手工输入、取消清理已通过；Google人工登录/验证码/完整保存、无桌面Linux仍未验收 |
| C04 凭据生命周期 | 已有实现＋外部验收 | 阶段1局部epoch撤销、阶段3采集CAS及关闭提交屏障；真实完整登录刷新未验收 |
| C05 浏览器隔离 | 已有实现＋外部验收 | 阶段3本机profile/进程/预算回收；真实双WAA共存不由单浏览器测试替代 |
| C06 browser generation transport | 条件未触发，当前不实施 | 原任务 `kind=conditional`；HTTP真实可用，没有证明需要浏览器传输的对照证据。不是原生接口必需项，不宣称已实现 |
| G04 工具/schema/思考 | 部分实现＋上游边界 | thought/signature/code/result已保真；函数工具、system和schema/thinking请求语义无已验证映射。可选参数按最新用户规则忽略，函数消息仍拒绝 |
| G05 目录/countTokens | 已有实现＋上游边界 | Business list/get/分页及生成路径已完成；countTokens明确未实现，不伪造usage |
| G06 高级参数激活 | 条件未满足，保持门禁 | 原任务 `kind=conditional`；六参数真实400。需新的逐字段widget合同及生效证据后才能启用，不自动重试探测 |
| F04 文件历史恢复 | 已有安全子集＋明确不支持项 | 阶段4已交付前缀、原session/fileIds复用及历史owner/MIME/hash/TTL校验；任意外部历史、重启后恢复、失效后换号重放仍不支持 |
| M07 非生成诊断 | 已有实现 | 阶段1逐模型状态、阶段2安全错误类别/退避/预算；读取状态不认证、不调用模型；最终Host权限用例仍保留未验收 |
| M08 导入预览/撤销 | **模型配置子集本地管理9/9通过，单测仍有阻断** | 原任务required。仅Business新模型/参数映射；同名跳过，资源与现有绑定不变。预览零写、来源hash/CAS拒绝、添加禁用未核实模型和单层撤销已在本地应用真实HTTP验证；全部配置内容摘要恢复，revision6→8，0次模型调用。[管理验收](../../runtime/business-local/completion-import-live.json)不替代[被AppControl阻断的config专项](../../runtime/business-local/completion-config-blocked.json)或未执行的Host单测。账号/密钥/Cookie导入及完整恢复仍排除，不能写完整M08通过 |
| Q02 SDK/REST | 已有实现与专项验收 | 固定Python2.32.0/JS2.22.0，阶段6双SDK实测4次成功；JS吞SSE错误限制要求检查STOP；新媒体专项待本轮记录 |
| Q03 旧主线三态回归 | 外部/环境验收未闭合 | 四接点门禁与历史组合记录存在；Host/API/app及历史其他OS阻断包未以本轮重跑替代；双WAA不能mock代验 |
| Q04 文件并发故障 | 已有离线实现＋环境边界 | Core/files/fileapi/gemini合成故障与真实文件续聊有证据；Host/resources最终用例不运行，race环境缺失明确保留 |
| Q06 构建流水线 | 选定范围通过，完整准入未闭合 | 阶段6build/vet和203项通过；本轮前端154项及typecheck/lint/build通过。新Go运行另见validation，不能把选定包通过写成全仓测试通过 |
| Q07 Business live | Argon限定范围已验收 | 阶段1–6有普通/SSE/文件/续聊和代码结果；其他模型/Auto、媒体输出、grounding/citation真实样本未验证。仍不设项目级live_verified=true |
| Q08 双WAA live | 外部验收 | 未执行也不自动调用旧账号模型；需相应环境及明确测试授权 |
| Q09 参数语义 | 已有保真/容错＋上游边界 | [最新可选参数规则](optional-parameters.md)覆盖原先“必须拒绝”旧条款；接受忽略不是支持。原六字段400保留，代码提示触发不等于tools映射生效 |
| I02 集成里程碑 | 本轮完成允许范围的集成记录 | 汇总实际实现、测试、重启与Argon结果；不能据此宣称58项全完成 |
| I03 回退与同步 | 手册已有，真实演练外部验收 | [maintenance.md](maintenance.md)和合成门禁已完成；无真实候选/完整回退操作，不能把备份二进制当作完整回退验收 |
| I04 整体真实准入 | 保持未完成 | 汇合Host/API等环境阻断、双WAA、人工登录、未知上游能力与完整升级/回退验收；本轮不发布部署 |
| S02 资源与故障边界 | 已有实现＋环境边界 | 阶段2及Core真实回收；resources/Host最终测试延期，race未执行；不承诺隔离OOM或整个进程崩溃 |
| U01 真实上游升级 | 外部验收 | 20项合成漂移和四接点门禁已有；未选真实候选SHA、未试合并/冲突处理/候选原测试，不自行在共享目录合并 |

其余29项保留原 `completed`/`offline_verified` 及对应历史证据。这不意味着其中涉及Host/API的全部最终案例已验收；每次最终报告仍应显式列出用户延期的Host/API/resources/app测试。

本轮新增环境阻断包括Gemini、files、token-count及config测试可执行文件被Windows Application Control拒绝运行，历史通过不能替代本轮修改后的测试。Host/API/resources/app继续未启动；不改名、不复制测试程序或改系统策略绕过。实际允许范围的结果、构建和合成本地验收由主窗口追加到validation。

最终媒体资源合同已区分方向：32项只限制user附件，model历史媒体/suggestions不消耗user槽位；两者的解码bytes仍共同计入50MiB，模型回合Part数、单blob编码/解码4MiB及请求共享预算照常生效。此项不扩大user MIME白名单，也不证明模型生成媒体能力。

## 本轮收尾边界

完成可证实的媒体 inlineData 传递、配置导入子集、对应离线/SDK/前端检查、统一重启和已授权Argon回归后，应结束连续开发并交付结果和限制清单。上游不提供、条件未触发及外部验收项固定列出，不通过估算token、工具模拟、生成重放、换上游或绕过系统策略消除缺口。官方依据见 [completion-protocol-evidence.md](completion-protocol-evidence.md)。
