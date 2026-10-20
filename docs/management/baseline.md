# 实施前基线

日期：2026-10-20。源代码基线：`00ad27b5947b0feb2f7fc4dde0c0853ac2a54200`。本轮未调用真实上游、模型或生产管理 API。

## Go

Windows / Go 1.26.0，在正常 Windows 执行上下文使用以下离线检查：

```powershell
$env:GOCACHE='C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-build'
$env:GOTMPDIR='C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp'
go test -p 2 -json ./internal/api ./internal/aistudio ./internal/app ./internal/config ./internal/requestdb ./internal/setup ./internal/proxydial ./internal/chromeauth
```

| 包 | 本轮结果 |
| --- | --- |
| internal/api | 未执行：Windows Application Control 阻止启动 api.test.exe |
| internal/aistudio | 通过 |
| internal/app | 通过 |
| internal/config | 通过 |
| internal/requestdb | 通过 |
| internal/setup | 通过 |
| internal/proxydial | 通过 |
| internal/chromeauth | 通过 |

整体命令 exit 1。`internal/api` 的错误为 `An Application Control policy has blocked this file.`，没有测试断言失败事件。本轮不修改或绕过该系统策略，不把编译完成当作测试通过。

原始日志位于被忽略目录 `runtime/management-development/baseline/go-test.jsonl` 和 `go-test.stderr.log`。

初次沙箱执行还曾因虚拟化 TEMP 路径不存在而在测试前失败；上表只记录正常执行上下文的最终结果。

## 前端

在 `web` 目录运行 `npm run lint`、`npm run typecheck`，两者 exit 0。日志为 `runtime/management-development/baseline/web-lint.log`、`web-typecheck.log`。

## 验收边界

以上结果只覆盖实施前离线基线。新增管理协议、崩溃恢复、跨语言、Manager、MySQL 和 Qt 验收须另行记录；未执行的 macOS、真实账号、真实模型和生产启用不能记为通过。
