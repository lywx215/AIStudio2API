## 第一阶段 H1–H5：2026-10-22 最新记录

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
