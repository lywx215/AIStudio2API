# Business R3 附加融合开发记录

分支 `dev-2`；宿主基线 `21596862b3805ab15ec184aa0037624c39c07626`；记录日期 2026-10-22。已接通本地应用并使用已有账号实测 Argon 原生生成、文件引用、流式与长上下文；[第一阶段热更新](phase1-hot-update.md)补充稳定账号运行时和逐模型状态判断。全量回归仍有 Windows 策略阻断及未实现项，不代表完整发布准入。58 项原始任务的逐项证据见 [tasks.json](tasks.json)，实际执行结果见 [validation.md](validation.md)。全局 `live_verified` 仍为 `false`。

固定设计参考为本机 `gemini-business-research/development-package-r3`，来源版本和归档摘要保存在 [source-manifest.json](references/source-manifest.json)。R3 参考包是方案证据，参考仓库自己的 live 报告不能作为 `dev-2` 的在线验收结果。

## 同一应用中的边界

一个 Go 程序、进程、监听端口和控制台；Business 复用现有 `consoleAuth` 实例与模型 API Key 验证。模型 Key 不能替代控制台登录。Business 自有配置、Google 凭据 Vault、账号、账号租约、会话和文件资源，原 AI Studio 的启动/停止、WAA、账号池、协议 DTO、文件和 Playground 保持原含义。

| 既有接点 | 允许的附加内容 |
|---|---|
| `internal/app/app.go` | `runServer` 创建/关闭 Business，向 API 配置注入可选扩展 |
| `internal/api/router.go` | Config 可选字段；完整原 handler 的两处最终返回外包一层桥 |
| `web/src/App.vue` | Business 导航组件、Tab 值与懒加载面板 |
| `web/src/types.ts` | `TabID` 增加 `business` |

新增宿主桥为 `internal/app/addon_business.go` 和 `internal/api/addon_business.go`；实现集中于 `internal/addons/business/{contract,config,host,vault,core,browser,dispatch,gemini,files,fileapi}`，界面集中于 `web/src/business`。桥外的 Business 代码不依赖旧生成服务来补齐能力。未命中请求用同一 Request、Body、ResponseWriter 委托原 handler 一次。

缺省配置 `enabled=false`。仅构造模块或读取缺省状态不会创建 `runtime/business`、迁移数据、启动浏览器或联网；显式保存配置/账号/上传才可能写入自有状态。配置损坏不会自动用默认值覆盖。关闭 Business 不删除数据、不启动或停止旧 WAA，也不把 Business 请求回退到旧账号池。

## 能力与当前证据

以下“已实现”表示对应源码存在；最终执行证据须以验证记录为准。

| 能力 | 当前范围与限制 |
|---|---|
| 配置/账号/Vault | revision/CAS、原子保存、加密账号快照和凭据 epoch；标签保存不改变凭据版本。热更新保留未变账号状态，显式替换仅撤销该账号租约。管理响应隐藏秘密；Windows 根密钥用当前用户 DPAPI，Unix 依赖 0700/0600 文件权限 |
| HTTP Core | Cookie→JWT 缓存、固定 HTTPS 上游、禁止授权跳转、每请求新会话、独立账号租约、动态并发/超时快照/取消与错误冷却；旧账号租约退出前不启用同 ID 新凭据，不自动重放已提交生成 |
| 原生 Gemini 文本 | 已有单个 user content 的文本生成、JSON 增量流和 SSE；有界解析/输出，只有上游成功终止证据才输出成功结束 |
| 参数预检 | 保留 JSON 字段存在性，拒绝重复/未知/冲突字段；`generationConfig` 仅接受带核实证据的逐字段映射，0/false 不当作未传 |
| 多轮、systemInstruction、tools | 当前拒绝；没有将历史或 system 拼成 prompt 冒充原生支持，没有默认启用全部工具 |
| usage/计数/高级输出 | 没有权威数据就不填充 token usage、模型版本或签名；`countTokens` 和未实现方法明确拒绝。schema/thinking 等可解析字段不等于真实上游语义已核实 |
| 模型映射 | 八个预置项只是显示名称，缺省禁用且 unverified；exact 需要真实 upstream ID，Auto 也需要核实依据，不猜 `modelId` |
| 文件 | 独立本地 broker、续传和 Host 已接通；在所选账号的新会话上传并绑定 fileIds。离线组合覆盖重启归属隔离，已有 Argon TXT/PDF 隐藏标记和数值实测，范围见 [local-live.md](local-live.md) |
| 浏览器采集 | 同控制台的主动采集入口已接通；观察用户自行登录，不发模型探测。fake driver 验证保存前清理、默认停用、revision冲突、取消及关闭；真实登录布局未验证 |
| 尚无证据的浏览器能力 | 远程无头接管、自动 modelId 观察、可选 browser generation transport 均未完成；当前传输是 HTTP |
| 控制台 | 原登录下懒加载配置/账号/模型能力面板、采集和独立原生试用已接通；逐模型分别显示配置有效、即时调度、认证缓存和实际成功证据，支持独立标签编辑。前端141项测试、typecheck、lint和build通过 |

`enabled` 表示配置意图；`generation_ready` 仅在至少一个模型的 AccountIDs 范围内有可用账号且并发容量充足时为真。`model_states` 分别给出 `configuration_valid`、`schedulable`、`auth_verified`、`model_verified` 和已观察时间；管理员填写的映射证据不等于成功调用证据，单模型成功也不代表全能力验证。状态查询不会刷新认证或发送模型请求。

## 同 Base URL 的 SDK / REST 选择

SDK 继续使用当前服务 origin、端口与模型 API Key。SDK 自行拼接 Gemini API 路径时，Base URL 填服务 origin，避免重复附加 `/v1beta`。在 SDK 的 HTTP options 中配置请求头 `X-AIStudio-Provider: business`，不需要在 Gemini JSON 中添加 `provider` 字段或建立第二个控制台登录。此头是本服务路由选择器，不是官方 Gemini 字段，也不是鉴权凭据。

| 请求 | 选择与处理 |
|---|---|
| 原有无 Business 标记的 `/v1/*`、普通 `/v1beta/*` | 原处理器及原默认目录 |
| `GET /v1beta/models` + Business 头 | 仅 Business 已启用绑定，独立分页；不合并旧列表 |
| `/v1beta/models/business-…:generateContent` / `:streamGenerateContent` | 模型命名空间即可命中；复用原模型 Key 验证 |
| `X-AIStudio-Provider: business` + 非 Business 模型 | 无旧别名归属时拒绝冲突，不猜选择；已有旧别名仍优先 |
| 无选择器的 `POST /upload/v1beta/files` | 原 AI Studio 新上传 |
| 同一上传路径 + Business 头 | 选择新的 Business 上传实现；支持原生 multipart/resumable，主体来自已验证的模型 Key |
| `?upload_id=biz-…` | 先于旧上传拦截器识别；必须验证实际签发能力、期限和 offset，前缀不授予权限 |
| 旧 `upload_id` | 即使带 Business 头也仍走旧实现 |
| `/v1beta/files/biz-…` | 明确 Business 资源及主体归属；不读取旧文件池 |
| `/api/addons/business/*` | 同一个控制台会话验证；状态/配置/账号管理，不接受模型 Key 替代登录 |
| 已有管理命名空间 | 永远优先原管理处理器 |

SDK 续传使用服务返回的 `X-Goog-Upload-URL`；不能假设续传会一直携带自定义头。Python `google-genai==2.32.0` 已通过零socket的生成/流/文件往返夹具；该版本会覆盖 `UploadFileConfig` 的 headers，因此 provider 头须放在 **Client 级 `HttpOptions.headers`**。夹具使用真实本地 handler 和合成 Generator；Host/API权限及Core上传由另外的离线组合测试覆盖，均不代表Google实测。JavaScript SDK 未验收。详细字段范围见 [capabilities.md](capabilities.md)。公开错误使用 Google JSON error envelope；流开始后的错误不能伪造成功终止。

生成请求在读取 JSON/base64 和本地文件前共用 `max_concurrent` 槽，避免只有上游调用受限。文件上传读体和下载写入限时2分钟；共享2个下载槽，满时先返回429。取消会推进实际HTTP读/写deadline，结束复位。Host关闭会等待整个操作，包括采集后的凭据提交；超时或浏览器清理失败会明确返回错误。

## 主线保护、回退与更新

操作步骤及真实候选验收表见 [maintenance.md](maintenance.md)。边界门禁逐一移除已审查的附加语句后，与固定基线比对；四接点内的其他改动同样失败。20 个合成用例验证移除关闭、改变管理优先级、换鉴权实例、破坏旧 Tab、扩大边界等会被拒绝。合成通过不能证明真实上游候选可合并或运行兼容。

```powershell
python -B -X utf8 scripts/business-addon/check_anchors.py
python -B -X utf8 scripts/business-addon/check_drift.py
```

Go/Web 最终回归由主窗口统一执行；文档/门禁子任务只执行离线检查。现有 `managedBy=lywx215` 构建和 `update_managed_test` 保留，不引入第二套更新器。原工作区 README/VPS 改动列于 [baseline.json](baseline.json)；根 README 必须保持 SHA256 `fa1a7d1a86885e2157fefb04a8332083e94f47eb6120b8ea9df0858b3d8c2f4b`。

用户已授权本地已有账号的 Argon 调用及应用重启，具体实测见验证记录。新的真实浏览器登录、双 WAA 在线验收、远程部署与外部审核未在本轮执行。同进程的可恢复错误可以约束；OOM 和整个进程崩溃无法由附加模块隔离。
