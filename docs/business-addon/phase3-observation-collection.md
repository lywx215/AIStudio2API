# 第三阶段：被动模型观察与远程采集

本阶段沿用后续移交中的 O1–O3 / C1–C4，而非最初 R3 任务表的阶段编号。三个 Codex 子代理已分派，但遇到账号用量限制，实际代码由主窗口继续完成。

## 实现

- 原控制台新增「远程浏览器采集」与「模型观察目录」。浏览器在服务器运行，控制台只接收限额 PNG，支持手工点击、文字、固定按键和验证码操作。
- 新任务绑定原控制台会话的不可逆身份及生命周期。退出登录、会话过期、10 分钟上限、60 秒无轮询/操作、应用关闭会结束任务。同一会话外不可查看/操控。管理接口仍受原有同源检查保护。
- 所有采集使用 Business 专属临时 profile、缓存、进程树与清理；只读复用浏览器二进制。Linux 无 display 自动使用 headless。没有调用旧 AI Studio Login/WAA，也未添加浏览器生成后端。
- 保存前重读 Cookie/URL/UA，确认浏览器已关闭且 profile 清理成功，然后以账户 revision CAS 写入加密 vault。保存账号默认为停用，替换会递增 credential epoch。清理失败不保存，并阻止再次启动。
- 支持账号独立的无认证 HTTP/SOCKS5 代理（显式端口）；代理同时用于采集浏览器及 HTTP 生成，保存于加密凭据内。当前不支持带用户名/密码代理，不会悄悄丢弃认证。HTTP 不继承全局代理，重新采集沿用账号 UA；新 profile 与旧指纹环境隔离。尚未实现可持久化的完整指纹配置。
- 新会话按已测清单识别 `camoufox 152.0.4-beta.29`，并检查 setViewport、storage、script/preload、screenshot、input 必要能力。未知版本不自动开放。

## 观察语义

浏览器仅在 Business 页面被动观察页面自己发出的 fetch/XHR；不主动请求模型目录、管理 OAuth API 或生成接口。只提取显式 `uiSettings.modelConfigInfo.resolvedModels[].modelId/displayName`，以及用户请求中的 `streamAssistRequest.assistGenerationConfig.modelId`。不读取或保存请求 headers、正文、JWT，也不根据显示名推断 ID。

观察包含账号、credential epoch、config ID、来源、时间、适配器版本。来源 URL 去掉 query/fragment；候选上限每范围 128 / 总计 1024，单次输入 256 KiB，目录限 4 MiB。有效期七天；同名不同 ID 标冲突，账号范围变化标陈旧。缺失/空 modelId 保留其请求形式但不推断为 Auto，不能确认启用。

所有新映射先进入 pending。用户确认只改变目录状态；不会修改模型绑定、不会自动启用，也不提升为实测验证。由于观察脚本位于网页环境，记录属于网页观察证据，不能代替上游调用验证。无法拦截的 Worker 请求、非 JSON 响应、未识别封装不猜测。

## 接口

- `GET /api/addons/business/browser-info`
- `POST /api/addons/business/browser`：账户 revision、可选 id、label、executable_path、headless、可选 proxy_url。
- `GET /api/addons/business/browser/{task}`：状态、缺失字段、到期时间、观察数。
- `GET .../{task}/frame`，`POST .../{task}/input`，`POST .../{task}/save`，`DELETE .../{task}`。
- `GET /api/addons/business/observations`，`POST .../observations/confirm`（revision + id）。

任务和截图不持久化。新增账号的观察先保留在任务内存中，只有账户保存成功后才写入目录。不自动启用刚采集的账号。

## 验收状态

离线验收覆盖：会话身份绑定/退出取消、跨会话拒绝、资源归还、CAS 迟到拒绝、清理先于保存、未知版本/缺失能力拒绝、映射冲突/过期/空 ID/敏感字段最小化。保留现有 Gemini SDK 及文件、流式回归。

Windows 本机实际 headless 测试发现并修复了启动参数混用、过短的初始化等待、退出后 SQLite 文件句柄延迟释放，以及浏览器真实报告 `camoufox 152.0.4-beta.29` 与旧 Firefox 版本假设不符。本机 headless 输入、截图及清理已通过。最终 Go 回归有 12 个测试包、217 项顶层测试通过；Host/API 测试程序被 Windows Application Control 阻止启动，用户选择保留这两项未验收记录，不再执行。最后新增的关闭清理错误传递案例因此尚未运行，较早通过的 Host/API 记录不能代替它。未绕过系统策略。

上述本机测试不能证明无桌面 VPS 登录/验证码/完整采集已验收。真实人工 Google 登录仍需操作员完成；合成页面和已有账号 HTTP 生成并不证明这一步。

最终运行与账号回归数据另见 `evidence/phase3-*`，以实际记录为准。

## 后续阶段

第四阶段仍需真实原生多轮/session 与逐参数证据：角色/历史前缀/账号 epoch 隔离，system 独立语义、工具回合和签名、响应 grounding/schema/thinking 及固定 SDK 双语言验收。当前容错接收可选参数只代表兼容请求形状，不能将忽略的参数列为已支持。
