# Business 原生计数与生成用量复核：2026-10-23

本轮扩展了[首次核对](token-count-protocol-evidence.md)的范围，重新下载公开协议，并核对已登录 Business 网页实际加载的 Standalone 脚本。脱敏机器记录见[本轮证据](evidence/token-recheck-20261023.json)。**仍未取得可实现原生 `countTokens` 或 `usageMetadata` 的 Business 上游合同。随后使用用户明确授权的临时 Bearer 完成一次 Argon 原始生成及同一测试会话回读，均未发现所检查的 token 用量字段。**

## ais03 更新账号后复核

2026-10-23 09:58:44–09:59:21 UTC，使用 `https://ais03.zeabur.app` 刚更新的原 Business 账号再次实测，账号凭据 epoch 为 2。线上部署为 `dev-2` 的 `0d8c00bda52a4367a040cb94db00be904d82744e`。完整脱敏结果见[更新后账号证据](evidence/token-ais03-updated-account-20261023.json)。

| 核对项 | 本次真实结果 |
| --- | --- |
| 账号认证、`lookupWidgetConfig` | 均 HTTP 200；使用现有配置，没有更改账号、模型绑定或应用部署。 |
| 新建合成会话、两轮 Argon 原始生成 | 创建与生成均 HTTP 200；两轮流分别 3 / 2 帧，末状态均为 `SUCCEEDED`。明确指定 `assistGenerationConfig.modelId=gemini-4-argon`。 |
| 每轮生成后的 `widgetGetSession` | 两次均 HTTP 200，并请求 answer/search 详情；生成、回读以及所检查的用量相关响应头均无候选字段。完整字段形状和未知数值路径均记录，没有依赖会丢弃未知字段的本地 DTO 得出此结论。 |
| ais03 原生 `countTokens` | `contents` 与完整 `generateContentRequest` 两种标准请求均为 `501 UNIMPLEMENTED`，不返回 `totalTokens`；模型目录也未宣称支持计数。 |
| ais03 原生 SSE | HTTP 200，13 帧 / 12 个文本分片，首文本 6.313 秒，文本到达跨度 2.609 秒，以 `STOP` 完成；所有帧均无 `usageMetadata`，也无本地估算响应头。 |
| 公开 Discovery 再下载 | `v1alpha` / `v1beta` / `v1` revision 仍为 `20261022`，分别 386 / 276 / 278 个方法，无 `countTokens` / `computeTokens`。 |

本次仅新增 3 次短生成：直接上游两轮，以及 ais03 的一次标准 SSE。更新后的 Cookie 与加密账号库、认证响应、JWT、管理密码和模型 Key 都只在进程内存处理；本机没有保存凭据或私有原始正文。认证与用量核对未修改生产账号、配置或状态判定实现，也没有部署或重启。生成后账号与 Argon 均 `ready`，认证及模型实测状态为 true。

**实施结论仍是原生合同未核实，不能启用原生计数适配。** ais03 的 `countTokens` 保持明确 501，未知生成用量保持省略；没有用 Gemma/SentencePiece、字符估算、文件摄取 `tokenCount` 或 `assistToken` / `uToken` 标识填充原生数值。工作区另一组未提交的本地估算统计改动不作为本次原生实现或验收依据，未部署到 ais03。

此次 Google 首页 GET 返回 302，遵守不跟随重定向的限制，未获取新的已登录前端资产；不能把前一轮缓存 bundle 当成本次重新下载的结果。公开 schema 和上述真实样本的缺失结果也不证明所有私有方法永久不存在。没有具体端点、输入语义、字段单位和流式累计合同，就不猜测私有计数路径或增加推测性解析器。本次受限探测的来源约束、脱敏、整数/十进制字符串候选、标识排除等离线检查通过；未将其扩大为全部源码、Host/API、SDK 或大上下文验收。

## 新增证据

| 来源 | 本轮结果 |
| --- | --- |
| Discovery Engine `v1alpha`、`v1beta`、`v1` | 三份新下载文档 revision 均为 `20261022`，区别于上轮 `20261013`；分别枚举 386、276、278 个方法，均无 `countTokens` / `computeTokens`。这些是各版本的方法数，不能相加作为独立方法总数。 |
| `biz-discoveryengine.googleapis.com` 的公开 Discovery 文档 | 无凭据 GET 成功；同样返回 revision `20261022` 的公共 Discovery Engine schema，386 个方法，root URL 仍为 `discoveryengine.googleapis.com`。不能把它当成完整私有 Widget 方法目录。 |
| 已登录页实际加载的中文 Standalone bundle | 23,646,039 bytes，SHA256 `c43305acaa3c2e9ab5cea6b8e7b11525d23722933c8f75c95923ba4cfa9c81bc`。提取 150 个不同 Widget 操作名称，没有计数、用量或配额操作；原生计数和生成用量相关 camelCase / snake_case 字面量均未命中。 |
| 重新下载的英文 Widget bundle | 13,333,329 bytes，SHA256 与首次核对相同：`4939333d6a81876631d6acb65573884f5f750897b9a94295f5766aaf36892a73`。提取 112 个 Widget 操作名称，没有计数或用量操作。 |

Standalone URL 从现有已登录 Google 网页的 `script.src` / preload DOM 属性取得；只下载其中公开的 `www.gstatic.com` 脚本，没有导出页面 bootstrap、浏览器 Cookie 或私有会话数据。各 URL、HTTP 状态、下载大小、SHA256、匹配项及协议字段列于机器记录。全部公开下载均不携带 Google 会话凭据，也不跟随重定向。

两份 bundle 的 `tokenCount` 都只有同一文件元数据映射中的两处出现，与 `fileId`、`byteSize`、`quotaPercentage`、`usedInConversation` 并列。另有 `maxTokens` 文件大小错误详情及 `browser_time_to_first_response_token` 首 token 延迟指标，均不构成模型 token 计数。Discovery 中的 AlphaEvolve 实验统计、NotebookLM 来源计数也不属于本次 Business 生成响应。

## 官方接口边界

[Enterprise StreamAssist](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.assistants/streamAssist) 及 [AssistAnswer](https://docs.cloud.google.com/gemini/enterprise/docs/reference/rest/v1alpha/projects.locations.collections.engines.sessions.assistAnswers) 的公开生成合同未提供对应 token 用量字段。`assistToken` 是请求与响应流的标识。`diagnosticInfo` 明确用于人工排错、结构可能变化，不能据其中未知数字猜测计费用量。

[Agent Platform CountTokens](https://docs.cloud.google.com/gemini-enterprise-agent-platform/reference/models/count-tokens) 确实存在，但使用 `aiplatform.googleapis.com` 的 publisher model 路径与 Cloud OAuth 身份；[Gemini Developer API](https://ai.google.dev/api/tokens) 使用另一套模型 API 与凭据。当前 Business JWT 的 audience、允许访问的 origin 和模型绑定未因此改变；本轮没有将它发送到这两套 API，也没有猜测私有计数路径进行轮询。

## 实施与验证

没有发现可冻结的新合同，因此未添加推测性生产解析器：已启用 Business 模型的 `countTokens` 继续明确返回 `501 UNIMPLEMENTED`，生成响应继续省略 `usageMetadata`。没有写入零、字符估算、其他模型 tokenizer 或文件摄取计数。

- Core 包回归通过；同次命令的 contract 测试程序被 Windows Application Control 阻止执行，保留未验收。
- 固定 `google-genai==2.32.0` 的 10 项无网络 MockTransport 测试通过。SDK 依赖只安装到忽略的本地 runtime 目录，未修改系统 Python。这些检查不替代最新上游或完整 Gemini Handler 验收。
- 本地旧 Cookie 会话对固定 `https://business.gemini.google/auth/getoxsrf` 返回 HTTP 401，未进入生成。Cookie 只发往该 Google origin，不保存认证响应或凭据。之后采用用户提供并明确授权的一次性临时 Bearer，成功完成下述直接上游检查。
- Host/API 及其他原策略阻断包未重试，继续未验收。没有部署、重启、修改生产账号或重复大 token 压测；项目级 `live_verified=false` 保持。

## 一次真实上游生成及回读

[脱敏实测记录](evidence/token-upstream-live-20261023.json)记录 `lookupWidgetConfig`、`widgetCreateSession`、`widgetStreamAssist`、`widgetGetSession` 均 HTTP 200。仅新建一个合成测试会话并调用一次生成；明确发送 `assistGenerationConfig.modelId=gemini-4-argon`，未重放用户 cURL 中的原会话或提示词。

原始流 4,029 bytes、3 帧，状态依次为 `IN_PROGRESS`、`IN_PROGRESS`、`SUCCEEDED`；同一测试会话回读 2,599 bytes。生成响应字段包含 `agentInfo`、`answer`、`assistToken`、`dataSourceInfo`、`modelInfo`、`sessionInfo`。对完整解析对象递归检查原生 usage/count 常见字段名，并归一化大小写和下划线，生成与回读均没有候选命中；准确字段清单见机器记录。没有保存原始正文、会话 ID、Cookie、Bearer 或任意估算值，也没有根据 `assistToken` 等标识填充计数。

本机结果页回传出现 `ConnectionAbortedError`，原始诊断收据保留此记录。Google 请求已经成功、脱敏结果已经落盘，因此将本机交付错误单独归类，没有再次发送模型请求。临时输入程序随后退出。该临时 Bearer 检查不更新 ais03 的账号 Cookie，也不替代新一轮完整适配器回归。

目前只有上述版本及这一成功样本的证据，不能据此断言私有服务永久没有其他方法。只有精确方法、模型与完整输入语义、字段单位和流式累计/终态语义都得到证实后，才启用原生适配；本轮保持 501 和未知用量省略。
