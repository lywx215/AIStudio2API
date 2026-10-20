# 控制台会话误退出与错误提示隔离

日期：2026-10-20。本地修复与验证完成；按用户要求提交并推送到 `dev` 分支，生产部署未执行。

## 修复内容

- 原生控制台只在 HTTP 401 且错误码为 `admin_login_required` 时触发登录失效。账号刷新、重新登录或其他上游认证失败保留当前页面，真正缺少或过期的管理员会话仍要求登录。
- 在解析错误码后再决定是否退出，避免批量刷新中的一个失败账号让整个控制台退出。非 JSON 或畸形 JSON 响应不再当作会话失效，也不把原始响应正文直接显示为错误提示。
- 公开 API 和原生控制台的错误消息使用按状态、错误类型选择的固定文案，不拼接上游错误、内部产品/通道名、真实模型名称、RPC、账号或内部地址。例如参数错误显示“请求参数无效，请检查请求格式与必填字段。”
- 覆盖 OpenAI、Anthropic、Gemini 普通错误，Chat/Responses/Interactions 等流式错误，后台响应读回、视频任务错误及实时连接错误。实时错误帧不再附带原始上游帧或文本；输入拒绝说明也使用固定文案。
- 保留 HTTP 状态、协议错误码、错误体结构、Retry-After/RetryInfo 和 Anthropic 的上下文超限识别信号。正常响应、显式模型目录/配置及生成内容不做文本替换。独立管理协议固定错误表未修改。
- 原始诊断在既有内部访问日志边界保存，对外消息由独立呈现函数生成；未改写上游异常本身。

## 实际验证

全部使用合成账号、假上游和临时数据，不调用真实模型、浏览器登录或生产接口。

| 检查 | 实际结果 |
| --- | --- |
| 新增 Go 定向回归及原管理员会话检查 | 通过，API 包 7.972 秒 |
| 最终 `go test -p 2 ./internal/api -count=1 -timeout=90s` | 通过，6.565 秒；包括真实 HTTP 401/会话分离、Embeddings、五类流式接口、后台读回、实时错误原始帧、诊断保留、重试/上下文信号 |
| 最终 API 包 vet | exit 0 |
| 前端 `node --test tests/*.test.mjs` | 36/36，无失败或跳过；含四项新会话回归 |
| 前端 `npm run typecheck`、`npm run lint` | 均 exit 0；首次新增测试的 lint 全局变量声明问题已修正 |
| 前端 Vite 构建 | exit 0，输出到独立临时目录，未覆盖本地运行产物；有既有图表包体积提示 |
| 全量 `go test -p 2 ./... -count=1 -timeout=2m` | exit 1；`requestdb.test.exe` 被 Windows Application Control 阻止启动，其断言本轮未执行。其余包通过，不能记为全量通过 |
| 全量 `go vet -p 2 ./...` | exit 0 |

系统拒绝原文：`An Application Control policy has blocked this file.` 未修改系统策略，也未更名、迁移或重新执行受阻程序来规避策略。

## 变更位置

- `web/src/api.ts` 与 `web/tests/admin-session.test.mjs`：会话判定和批量/单账号负向回归。
- `internal/api/error_presentation.go`：固定错误消息目录；`errors.go`：HTTP 输出边界。
- `internal/api/openai.go`、`anthropic.go`、`gemini.go`、`interaction_output.go`、`responses.go`、`responses_background.go`、`live.go`、`video.go`：其他错误输出边界。
- `internal/api/error_presentation_test.go`：完整内部标记不得出现在对外错误、原始诊断仍保留、协议状态和恢复提示仍有效。

Windows 为本轮执行环境；未执行 macOS 实机或线上验收。截图中外部调用方自行配置的渠道名称由调用方系统管理，本修复控制的是本服务返回的错误内容。
