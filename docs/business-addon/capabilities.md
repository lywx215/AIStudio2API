# 当前原生 Gemini 能力合同

本轮收尾的官方边界见 [协议证据](completion-protocol-evidence.md)，原58项任务的逐项剩余处置见 [收尾清单](completion-roadmap.md)。第六阶段已修正文档来源为retrievedContext，补齐累计终帧引用定位和citation来源信息，详见 [第六阶段合同](phase6-native-metadata.md)。第五阶段的 [代码/结果及双SDK合同](phase5-native-output.md) 和第四阶段的 [精确前缀续接合同](phase4-native-conversations.md) 继续适用。高级参数语义和原生计数仍未开放。

后续定向核实发现公开 widget 的独立思考参数路径，但当前 Argon 元数据未提供可用 level ID，因此没有启用猜测映射。空 Google Search 工具已有明确请求合同，并新增每次请求的同账号供应商核实；详见 [工具证据](tools-protocol-evidence.md)、[高级参数证据](advanced-parameter-evidence.md) 和 [原生计数证据](token-count-protocol-evidence.md)。具体运行结果以 validation 最新记录为准。

新增[延迟引用关联](advanced-tools-count.md)：metadata-only消息可通过同replyId对应已交付普通正文，所有segment的text和UTF-8 offset必须精确匹配。思考文本保留但不计入正文offset，实际Part索引仍包含思考；没有身份或无法定位时只保留来源。该子集的真实运行结果与历史合成测试分开记录。

**运行版本限制：最新thought引用修复的应用构建被Windows应用控制阻止启动，已回退上一允许构建；当前运行版本仍有同ID thought导致source-only的限制。** 新修复的104项Core/contract测试通过不替代真实验收，严格搜索grounding尚未记为通过。见validation最新记录。

2026-10-22 参数策略更新：用户要求不兼容可选参数直接忽略。顶层可选字段以及没有已验证映射的生成字段不再导致请求拒绝；已支持字段、消息结构和文件/资源校验保留。详见 [可选参数容错](optional-parameters.md)。下文历史真实探测结果不因这项改动改变。

2026-10-22 源码快照；实现存在与运行通过分开。本地 Argon 标准生成、TXT/PDF引用、增量SSE和精确前缀续聊已有真实证据；第六阶段固定 Python `google-genai==2.32.0` 与 JavaScript `@google/genai@2.22.0` 共4次生成成功，Python实际返回代码及执行结果，见 [第六阶段汇总](evidence/phase6-summary-20261022.json)。该轮未返回grounding/citation来源，相关转换只有离线证据。六个高级参数真实转发被上游拒绝，见 [早期参数实测](evidence/local-argon-live-20261022.json)。项目级 `live_verified=false` 仍表示58项整体计划未全部认证。本表描述 Business 分支，原 AI Studio 请求、参数和响应继续由原处理器决定。

## 请求预检

来源：`internal/addons/business/gemini/request.go` 与 `config/config.go`。JSON 保留 raw 值，拒绝重复字段；支持的 camelCase/snake_case 别名不能同时出现。预检发生在账号租用/Google认证/上游会话/附件上传前，文件 resolver 在字段检查通过后读取本地快照。

| 原生字段 | 当前合同 |
|---|---|
| `contents` | 最多4129条原始片段，相邻model合并后最多33条user/model内容，以user结束；相邻user拒绝。多轮须匹配本进程已交付前缀，否则409；不拼接历史、不重放生成 |
| `contents[].parts[].text` | 字符串，保留顺序；null 不作为空字符串 |
| `inlineData` / `inline_data` | 显式 MIME 与 base64，严格解码/大小/格式校验；解析不证明真实上游媒体能力 |
| `fileData` / `file_data` | 仅 Business broker 的资源；校验本 origin/主体/MIME/内容摘要，读不可变本地快照；历史引用也必须仍有效，不下载任意 URL 或读取旧文件池 |
| 混合 parts | 每个 part 只使用一种支持类型；user 消息中的附件与文本必须各自连续。模型历史保留服务返回的完整 text/thought/signature/inline媒体/suggestions/code/result part 顺序与边界 |
| `generationConfig` / `generation_config` | 仅转发存在已核实且有 evidence 映射的字段，其余忽略；已支持值仍校验，保留 0/false，不注入采样默认值 |
| `tools:[{googleSearch:{}}]` / `google_search` | 只支持空配置；同账号 lookupWidgetConfig 确认为 Google Search 才发送 toolsSpec.webGroundingSpec={}，不自动打开其他工具，不重试。供应商不符400；配置读取失败按真实错误返回。模型是否真正检索须看返回的结构化 grounding，HTTP200不能证明 |
| 其他 `tools`、顶层 `systemInstruction`、`toolConfig`、`safetySettings`、`cachedContent`、`labels` 及未知可选参数 | 未映射项接受并忽略，不模拟成prompt。Google Search 时间/类型限制、同一Tool混合其他字段或非空共享toolConfig时整个搜索工具忽略，不删限制后搜索；tools最多64项，非法类型/重复搜索/冲突别名400 |
| `contents` 中的函数调用/响应 | 当前仍拒绝，不通过删掉消息内容来继续生成 |
| 历史 model 中 thought/signature | 仅用于完整前缀身份匹配，保留原 part，不重新发送历史或恢复外部签名；当前user未知/函数part仍拒绝 |

可登记的 generation 字段为 `temperature`、`topP`、`topK`、`maxOutputTokens`、`stopSequences`、`candidateCount`、`seed`、`presencePenalty`、`frequencyPenalty`、`responseMimeType`、`responseSchema`、`responseJsonSchema`、`thinkingConfig`、`responseModalities`、`mediaResolution`。有证据的映射才参与类型/范围/组合检查与转发；登记表出现字段不表示 Business 上游支持。默认所有绑定均无参数映射；不自动启用工具、fan-out、多候选或本地截断来假装原生兼容。`imageConfig`、`speechConfig`、logprobs 等未实现可选项忽略。

源码资源限制：原生请求最多68 MiB、归一化33条content；每条user最多256 parts，每个合并后的模型回合最多4096 parts。完整历史文本、代码、结果与签名合计1 MiB；所有user附件加model历史媒体/suggestions的解码bytes合计50 MiB。32项附件数量上限只计user的inlineData/fileData，model历史媒体和suggestions不占这32个槽，仍受每模型回合4096 parts、每blob编码加解码4MiB以及请求/共享资源预算限制。用户附件 MIME 白名单为 `text/*`、PNG/JPEG/WebP/HEIC/HEIF、PDF/JSON/XML；text/plain和application/pdf在较早小文件引用实测中通过，其余MIME及真实上限压力未验证。输出媒体扩展没有同步扩大user附件白名单。

较早单次长上下文专项验证通过：370092字节输入、24554字节输出；Google文件摄取计数分别153363与6816，5处随机检查点正确，257文本块持续90.672秒到达并正常STOP。该辅助计数不是Gemini原生 `countTokens` 或Argon计费用量；没有据此开放 `countTokens`、提高限制或宣称最大上下文已测试，也不能代替第四阶段验收。见 [专项证据](evidence/argon-long-context-20261022.json)。

上游参数名可以与原生字段名不同，但必须是有界的简单字段名，带 verified/evidence；不能映射到模型选择字段 `modelId`。已知原生字段间的映射还检查值类型，原始schema键名、大整数和0/false保留。登记“已核实”是管理员的配置意图，不会将 `live_verified` 改为true。

Google 官方 [AssistGenerationConfig](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/AssistGenerationConfig) 及替代 [generationSpec](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist) 仅列出 `modelId`。先前授权实测中，widget上游HTTP400逐项报temperature、topP、topK、maxOutputTokens、responseMimeType、thinkingConfig为未知字段；实际请求体保留0/false及原始值，映射未开启。其他高级字段未实测，保持未核实门禁。参考池旧报告的“pass”是HTTP400预检拒绝或mock，未用作参数生效证据。

公开 widget 实际使用 `generationSpec.thinkingConfig.thinkingLevel`，其值来自目标agent扩展的 supportedThinkingLevels，而不是任意套用LOW/HIGH。当前账号的Argon模型目录和agent列表未提供此信息；不能将公开路径存在等同于Argon参数已支持。includeThoughts也没有实施本地过滤或上游控制。原生countTokens继续501，usageMetadata缺省；文件摄取tokenCount及Cloud publisher-model计数均不替代本Business账号的原生计数。

## 文件、会话与流

| 范围 | 实现与限制 |
|---|---|
| 模型目录分页 | Business list/get保持独立目录；pageSize缺省50，正32位整数超过1000按1000处理，显式0/负数/非法值返回400 |
| 原生 Files | `fileapi/handler.go` 实现 multipart/resumable、list/get/delete/download 与本 origin resolver；固定Python/JS SDK往返离线专项已通过。Host/API权限及重启组合测试有较早记录，最终Host/API两项仍保留未验收 |
| Broker | `files/store.go` 独立 bytes/元数据、主体、随机高熵 upload capability、offset/期限/query/cancel/finalize；文件和续传期限分别缺省 48h / 30min，存储缺省上限 1 GiB、4096记录 |
| 持久主体 | API桥验证模型 Key 后，由 Business Vault 根密钥派生非可逆 owner；缺省禁用不为此创建目录，重启稳定及不同Key隔离有较早离线组合通过记录 |
| 账号/session物化 | 首轮在选定账号的新session上传；已交付精确前缀续接复用原session/file IDs，只上传末条消息新增附件。绑定主体、模型、完整配置及账号凭据版本，不写进本地broker，不跨账号/session重用 |
| 续接与恢复 | 仅接受本进程完整交付的精确历史前缀；内存缓存15分钟、全局128条/每主体16条，一个前缀只能消费一次。缺失、修改、过期或歧义前缀返回409；不重放生成、不自动换号或新建session恢复，不支持任意外部历史导入 |
| 删除与URI | 本地删除不代表上游删除；resolver 不执行网络下载，不暗用旧账号凭据，不跨 origin 跟随授权 |
| 生成响应 | `gemini/handler.go` 原生 candidates/content.parts，JSON/SSE增量；`core/stream.go` 只有收到成功终态后才给成功完成 |
| 网页累计帧兼容 | 接受可选 `role:model`；成功终帧只有完整匹配已输出Part前缀才去重。连续普通文本可重分片，typed/thought/signature/inline边界逐项精确匹配；失配保留整帧 |
| 建议辅助数据 | `application/json+suggestions` 保留为原生 `inlineData`，严格验证标准 base64/UTF-8/JSON，编码及解码大小同计 4 MiB 输出预算；Python/JS SDK离线普通/流式解码通过，较早长上下文实测记录过1个辅助inline part，不代表所有辅助数据语义已验证 |
| 原生媒体inlineData | 本轮实现有界bytes透传，MIME/bytes及thought/signature保留同一Part；不拼接媒体、不执行解码或外链下载。生成输出与历史回显使用同一白名单和摘要规则；收尾版新增Gemini/SDK专项有系统策略阻断，真实Argon媒体输出尚未验证 |
| 代码和执行结果 | 保留上游实际返回的PYTHON代码及OK/FAILED/DEADLINE_EXCEEDED结果、thought/signature；输出及续聊均计入预算。不会本地运行代码；不等于原生tools参数生效 |
| 来源引用 | documentMetadata投影retrievedContext URI/title；同回复文本及可精确匹配的累计终帧通过字节映射拆分跨Part支持区间。无内容元数据只保留来源。成功帧仅一次，128来源/512区间/512KiB |
| Citation | source-only URI/license投影为citationMetadata.citationSources，不复制不明偏移、不补title/date；128来源/128KiB，和grounding共同计入4MiB输出预算 |
| 流式Chat交付 | 保留最后一个真实Part随STOP交付，避免SDK丢失完整模型回合；支持SDK相邻model片段归一化。失败无STOP/无成功前缀 |
| 输出未支持项 | 不伪造 usage/token count/modelVersion/签名或引用分数；`countTokens` 和未知方法明确未实现；生成文件fileId的授权下载、白名单外媒体、自定义工具及其他引用类型仍未完成 |
| 配置导入/撤销 | 本轮实现仅模型配置子集，preview零写、来源hash/revision绑定、只追加禁用未核实模型、同名跳过；原有效console内单层内存撤销，后续配置保存/重启后不可用。资源/账号/凭据不导入。[本地真实管理回归](../../runtime/business-local/completion-import-live.json)9/9通过，合成禁用模型添加后撤销，配置内容摘要恢复且revision6→8，模型调用0次；[config单测启动被策略阻断](../../runtime/business-local/completion-config-blocked.json)，Host单测未执行，不能以管理回归替代。接口和限制见 [协议证据](completion-protocol-evidence.md) |

输出媒体白名单以 `contract/media.go` 为准：PNG/JPEG/WebP/GIF/HEIC/HEIF；WAV/x-wav/MP3/MPEG/MP4/AAC/OGG/FLAC/WebM音频；MP4/WebM/MPEG/QuickTime视频；PDF；以及已有suggestions。`audio/pcm`、`audio/l16`只接受可选rate（8000–192000）、channels（1–8）和codec=pcm；没有提供时不补造。MIME最长256字节、仅可打印ASCII，除PCM上述字段外不接收参数。原始MIME保真，bytes非空且严格base64，总输出仍限4MiB；此列表是传输适配范围，不是模型产图/产音能力承诺。

第三阶段已接通控制台远程无头画面、手工输入和被动模型观察，需明确通过版本/能力校验的 Camoufox 程序路径及 Business 自有临时 profile。本机 `camoufox 152.0.4-beta.29` 的无头输入、截图、清理已实测；最终应用实际打开 Business 登录页并接收 Tab 操作，取消后profile和预算归零。storage API 可读 HttpOnly Cookie；URL只观察明确 config/csesidx，缺字段不猜。观察到的模型先进入待确认目录，不自动修改绑定；真实模型目录目前仍为空。人工 Google 登录/验证码/完整账号保存及无桌面 Linux VPS 仍未验收，browser generation transport 未实现。新/替换采集账号先保持 disabled，由用户核对后启用。第三阶段历史前端记录为152项测试及typecheck/lint/build通过，详见 [阶段三记录](phase3-observation-collection.md)。

固定 JS SDK 2.22.0 可能将 HTTP200 SSE 的 `error` 转为空对象并正常结束迭代；调用方必须确认实际收到成功 `STOP`，不能仅依赖迭代器结束。服务端保持错误包且不补造终态；[JS消费检查示例](../../scripts/business-addon/sdk-js/README.md)提供显式检查，不重试生成。

## 证据边界

`gemini/*_test.go`、`files/store_test.go`、`fileapi/*_test.go`、`core/attachments_test.go`、Host/API测试和固定SDK脚本属于离线执行入口；测试文件存在并不证明运行成功。SDK夹具挂接真实Gemini/file handler与假Generator，不覆盖Google模型、宿主认证或完整HTTP Core，不能拿它代替Host/API/真实账号验收。Host/API/resources/app按用户要求保持未验收；本轮新增Gemini、files、token-count及config运行也受到Application Control阻断，历史通过记录不替代当前新增或修改的用例。具体执行、跳过和环境阻断统一记录在 [validation.md](validation.md)。
