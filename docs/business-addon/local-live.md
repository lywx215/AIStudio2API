# 本地 Argon 验收接续

2026-10-22：用户确认系统放行后，本地应用已启动并保持运行。已有账号的Argon普通JSON、TXT/PDF引用、增量SSE通过；高级参数探测被Google逐项HTTP400拒绝。详见 [验证记录](validation.md) 和 [脱敏证据](evidence/local-argon-live-20261022.json)。下列脚本可复现测试；真实生成会使用账号配额，不由启动脚本自动执行。

目标实例为 `runtime/business-local/`，独立运行状态，监听 `127.0.0.1:2048`，旧 AI Studio `AUTO_START=false`。原 Python pool 的 8000 端口继续保留。模型对外名称为 `business-gemini-4-argon`，Google实际ID为 `gemini-4-argon`。

- `scripts/business-addon/start-local.ps1`：隐藏启动既有构建，复用仓库同版本Camoufox缓存。模型 API Key 从参考池配置读入内存并传入子进程环境，不写入参数或配置文件；控制台仅绑定回环。发现端口已占用会停止，不终止现有进程。
- `scripts/business-addon/live_acceptance.py bootstrap`：通过固定回环管理接口把现有账号写入 AES-GCM/DPAPI vault，启用Argon精确绑定。只在空账号库导入，不打印凭据。不会启用高级参数映射或触发模型。
- `unary`：固定 Python SDK 2.32.0 查询模型，并验证短JSON响应和成功终态。
- `sse`：SDK流式请求，记录各事件时间、文本块数、首/尾标记及STOP；还要求文本到达时间跨度至少0.1秒，不能仅凭多个事件宣称增量到达。
- `files --fixtures <expected.json>`：SDK原生上传TXT/PDF，以fileData引用，要求从附件读取隐藏标记并求和；随后删除本地测试文件。夹具由本次工作生成，提示词不含答案。
- `preflight`：验证无证据高级参数明确HTTP400，不调用Google。它不是上游参数生效证明。
- `scripts/business-addon/parameter-probe`：另行显式 `--allow-live` 的一次性上游字段探测，读取现有加密vault，记录实际发送的生成字段及上游HTTP状态/未知字段名；不保存JWT、Cookie或原始响应，不修改生产映射。已实测上游拒绝六个高级字段；诊断器兼容流式错误数组。

Python入口需参数 `--session <已有参考池JSON路径>`、`--report <本地报告路径>`。默认 origin 为 `http://127.0.0.1:2048`，仅允许IPv4回环，不跟随重定向，不使用环境代理，不自动重试。SDK provider头放在客户端级。进程凭据不会写入Git、命令行或报告。

应用产物：`runtime/business-local/aistudio2api.exe`，SHA256 `3E955E4AFEEF0A4D1F48C005ECCF68871526FB45EAFEF12C55FD7229C9451270`。构建之后未修改业务源码。离线组合测试10个包通过；本次4次应用生成成功，另有2次上游参数探测拒绝。其他模型、10万token压力、多轮/工具及完整发布计划不在本次完成结论内。

后续长上下文专项已完成：`long_context.py --allow-live --session <参考池JSON> --vault <加密vault目录> --counter <token-count.exe> --run-dir <新目录>` 先计输入、再进行一次本地标准SSE生成、最后计原始输出。实测输入153363/输出6816（文件摄取token），5个随机检查点及257文本块/STOP通过。`verify_long_context.py <run目录>` 可离线重建SSE、校验请求/输出/计数hash并验证Markdown表格中的原始数据，完全不联网。详见 [验证记录](validation.md)。
