# Business 可选参数容错

2026-10-22 根据用户要求，将不兼容的可选参数改为忽略。该规则仅作用于 Business 请求解析，不改变原 AI Studio 主流程。

- 顶层尚未实现的可选字段，例如 `safetySettings` / `safety_settings`、`systemInstruction`、`toolConfig`、`cachedContent`、`labels`，接受后忽略；空数组和 null 同样不会触发“不存在已验证映射”的 400。
- `tools` 新增空 `googleSearch:{}` / `google_search:{}` 子集：使用同一账号只读核实 Google Search 供应商后，转为 `toolsSpec.webGroundingSpec:{}`。不自动启用其他工具；非 Google 或未核实供应商明确拒绝，不静默降级。带时间范围、搜索种类限制或同一 Tool 内混合其他字段时整个变体仍忽略，不丢掉限制后发起无限制搜索。重复搜索、冲突别名或非法类型返回400；空数组/null保持可接受。函数声明、代码执行请求开关等未映射工具仍忽略，不能视为已执行。
- `generationConfig` / `generation_config` 中没有映射、未验证或缺少证据的字段不发送到上游。其他已经验证的字段继续保持原值传递，包括 0、false 与 schema 中的大整数。
- 搜索同时受顶层 `toolConfig` / `tool_config` 约束。仅缺省、null或空对象允许启用；非空配置（包括语言、位置、调用模式）使整个搜索工具保持忽略，避免删掉共享限制。请求空搜索时，配置类型错误或别名冲突返回400。
- 当前无法执行的多候选、非文本输出和包含未知子字段的 `thinkingConfig` 会忽略对应生成选项。忽略不表示该功能已实现，模型使用上游默认行为。
- JSON 语法、重复键、冲突别名、已支持字段的非法类型/范围、消息结构、附件权限、MIME 和大小限制继续校验。函数调用消息不会被静默删掉；第四阶段多轮仅续接已完成的精确历史前缀，不能匹配时明确返回409。
- 不因上游报错自动移除参数并重发生成，避免重复请求和重复计费。

普通 JSON、SSE 和 JSON 数组流使用同一规则。Python SDK 离线契约同步覆盖携带安全设置、system instruction、tools 和未映射采样参数的请求。

旧的 `live_acceptance.py preflight` 专门验证参数被拒绝。容错后这些请求可能产生真实生成，因此入口已停用并在任何网络请求前退出；历史实测记录仍保留当时的原始结果。

本文件说明源码行为；线上站点只有部署包含该修改的版本后才会生效。

## 本次验证

- `go test -mod=readonly ./internal/addons/business/gemini` 通过，已设置 `BUSINESS_GENAI_SDK_PYTHON` 执行固定 Python SDK 2.32.0 契约。专项覆盖 11 组可选参数在普通 JSON、SSE、JSON 数组流中的处理，以及映射值保留、模型选择、格式和附件校验。
- `go test -mod=readonly ./internal/addons/business/fileapi` 通过。正常用户权限下执行这两项后，沙箱内的临时文件存储错误未复现。
- 同次运行的独立 `files` 包测试程序被 Windows Application Control 阻止，未将其计为通过，也没有移动或改名绕过系统策略。
- Python 脚本语法检查通过；退役的 preflight 已验证会在读取账号文件和网络请求之前退出。
- 本次没有推送、重新部署或发起线上模型调用。
