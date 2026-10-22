# 当前原生 Gemini 能力合同

2026-10-22 源码快照；实现存在与运行通过分开。用户放行后本地 Argon 标准生成、TXT/PDF引用、增量SSE真实通过；高级参数转发后被上游明确拒绝，见 [实测证据](evidence/local-argon-live-20261022.json)。项目级 `live_verified=false` 仍表示58项整体计划未全部认证。本表描述 Business 分支，原 AI Studio 请求、参数和响应继续由原处理器决定。Python SDK固定为 `google-genai==2.32.0`，其零socket契约及本次真实调用均通过。没有固定或验收 JavaScript SDK 版本。

## 请求预检

来源：`internal/addons/business/gemini/request.go` 与 `config/config.go`。JSON 保留 raw 值，拒绝重复字段；支持的 camelCase/snake_case 别名不能同时出现。预检发生在账号租用/Google认证/上游会话/附件上传前，文件 resolver 在字段检查通过后读取本地快照。

| 原生字段 | 当前合同 |
|---|---|
| `contents` | 必须只有一个 user content；不把多轮历史压缩成 prompt |
| `contents[].parts[].text` | 字符串，保留顺序；null 不作为空字符串 |
| `inlineData` / `inline_data` | 显式 MIME 与 base64，严格解码/大小/格式校验；解析不证明真实上游媒体能力 |
| `fileData` / `file_data` | 仅 Business broker 的资源；校验本 origin/主体/MIME，读不可变本地快照；不下载任意 URL 或读取旧文件池 |
| 混合 parts | 每个 part 只使用一种支持类型；附件与文本必须各自连续，不接受交错后偷偷改变语义 |
| `generationConfig` / `generation_config` | 每字段需存在已核实且有 evidence 的映射；未传、0、false、null 分开处理；不注入采样默认值 |
| `systemInstruction`、`tools`、`toolConfig`、函数调用/响应、`safetySettings`、`cachedContent`、`labels` | 当前拒绝；没有模拟成 prompt 或借旧 Gemini DTO 暗中接受 |
| 请求 thought/signature、未知字段/未知 part | 当前拒绝；响应保留上游证实的 thought/signature 不代表接受请求侧恢复思考 |

可登记的 generation 字段为 `temperature`、`topP`、`topK`、`maxOutputTokens`、`stopSequences`、`candidateCount`、`seed`、`presencePenalty`、`frequencyPenalty`、`responseMimeType`、`responseSchema`、`responseJsonSchema`、`thinkingConfig`、`responseModalities`、`mediaResolution`。类型/范围/组合检查与逐字段证据均需通过；登记表出现字段不表示 Business 上游支持。默认所有绑定均无参数映射；不自动启用工具、fan-out、多候选或本地截断来假装原生兼容。`imageConfig`、`speechConfig`、logprobs 等未实现项拒绝。

源码资源限制：原生请求最多 68 MiB、文本合计 1 MiB、附件合计 50 MiB、最多 32 个附件。附件 MIME 白名单为 `text/*`、PNG/JPEG/WebP/HEIC/HEIF、PDF/JSON/XML；text/plain和application/pdf的本次小文件引用已真实验证，其余MIME及上限压力未验证。MIME 白名单不等于生成多模态输出能力。

追加单次长上下文验证通过：370092字节输入、24554字节输出；Google文件摄取计数分别153363与6816，5处随机检查点正确，257文本块持续90.672秒到达并正常STOP。该辅助计数不是Gemini原生 `countTokens` 或Argon计费用量；没有据此开放 `countTokens`、提高限制或宣称最大上下文已测试。见 [专项证据](evidence/argon-long-context-20261022.json)。

上游参数名可以与原生字段名不同，但必须是有界的简单字段名，带 verified/evidence；不能映射到模型选择字段 `modelId`。已知原生字段间的映射还检查值类型，原始schema键名、大整数和0/false保留。登记“已核实”是管理员的配置意图，不会将 `live_verified` 改为true。

Google 官方 [AssistGenerationConfig](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/AssistGenerationConfig) 及替代 [generationSpec](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist) 仅列出 `modelId`。本轮进一步真实探测：当前widget上游HTTP400逐项报temperature、topP、topK、maxOutputTokens、responseMimeType、thinkingConfig为未知字段；实际请求体保留0/false及原始值，映射未开启。其他高级字段未实测，保持未核实门禁。参考池旧报告的“pass”是HTTP400预检拒绝或mock，未用作参数生效证据。

## 文件、会话与流

| 范围 | 实现与限制 |
|---|---|
| 原生 Files | `fileapi/handler.go` 实现 multipart/resumable、list/get/delete/download 与本 origin resolver；已通过固定Python SDK往返和Host/API权限/重启离线组合测试 |
| Broker | `files/store.go` 独立 bytes/元数据、主体、随机高熵 upload capability、offset/期限/query/cancel/finalize；文件和续传期限分别缺省 48h / 30min，存储缺省上限 1 GiB、4096记录 |
| 持久主体 | API桥验证模型 Key 后，由 Business Vault 根密钥派生非可逆 owner；缺省禁用不为此创建目录，重启稳定及不同Key隔离已通过离线组合测试 |
| 账号/session物化 | `core/attachments.go` 在选定账号及新 session 后上传；Google file ID 只在本次租约中使用，不写进本地broker，不跨账号/session重用 |
| 重新上传与恢复 | 每个新请求从不可变快照重新上传；失败不提交生成、不自动换号重放。多轮历史恢复未支持，不能据此宣称全部 F04 完成 |
| 删除与URI | 本地删除不代表上游删除；resolver 不执行网络下载，不暗用旧账号凭据，不跨 origin 跟随授权 |
| 生成响应 | `gemini/handler.go` 原生 candidates/content.parts，JSON/SSE增量；`core/stream.go` 只有收到成功终态后才给成功完成 |
| 网页文本帧兼容 | 接受可选 `role:model`；成功终帧仅对普通无签名文本做精确累计前缀去重，保留 thought/signature 和 part 顺序；已通过离线测试 |
| 建议辅助数据 | `application/json+suggestions` 保留为原生 `inlineData`，严格验证标准 base64/UTF-8/JSON，编码及解码大小同计 4 MiB 输出预算；Python SDK 普通/流式解码通过，真实 payload 尚未验证 |
| 输出未支持项 | 不伪造 usage/token count/modelVersion/签名/grounding；`countTokens` 和未知方法明确未实现；输出媒体与自定义工具未完成 |

浏览器仅支持服务端本地桌面的可见用户登录采集，需显式兼容 Camoufox Firefox 152 程序路径及 Business 自有 profile。storage API 可读 HttpOnly Cookie；URL只观察明确 config/csesidx，缺字段不猜。远程无头接管、自动 modelId 观察和 browser generation transport 均无完成证据。新/替换采集账号先保持 disabled，由用户核对后启用；本轮没有启动真实浏览器。

## 证据边界

`gemini/*_test.go`、`files/store_test.go`、`fileapi/*_test.go`、`core/attachments_test.go`、Host/API测试和固定SDK脚本属于离线执行入口；测试文件存在并不证明运行成功。SDK夹具挂接真实Gemini/file handler与假Generator，不覆盖Google模型、宿主认证或完整HTTP Core，不能拿它代替Host/API/真实账号验收。具体执行、跳过和环境阻断统一记录在 [validation.md](validation.md)。
