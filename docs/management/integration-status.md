# 双项目并行实施总控记录

**当前状态：六项修复已完成，离线/合成联合验收通过。** 本轮授权、责任分配、复现及完整结果见 [remediation-20261020.md](remediation-20261020.md)。真实 MySQL、平台实机与生产范围仍未验收。下文首次开发记录保留为历史基线。

2026-10-20。调度目录和任务 ID 见 `windows.json`，ownership 与验收范围见 `parallel-tasks.md`。全部开发窗口使用 GPT-6.1 Sol / xhigh，共用各自正式项目工作树；没有新建分支/worktree、提交、推送、部署或生产迁移。

## 调度变更

用户明确要求加速，将 AIStudio2API 和 gemini-manager 作为两条并行开发线。已创建 Go G0–G6 七个正式项目窗口、Manager M1–M4 四个正式项目窗口，取消旧的“Go 全验收后才开始 Manager 开发”顺序。公共契约先冻结，独立模块提前并行，真实组合验收仍单独执行。

## 六项修复的最终验收

Go 最终 322 个 Go/SQL/模块输入前后相同；全仓 test/vet、Windows CLI build、Linux amd64 cross-build 通过。G6 使用最终 fixture 二进制完成 22 项真实 HTTP × Python 客户端检查及 7 项原生集成检查，全部通过。Linux 交叉编译不代表运行验收。

Manager 模块检查：M1 仓储/迁移 46 项、M2 新后端 40 项、原有 Aitoapi 187 项、M3 新旧 UI 57 项通过。M4 另完成 15 项实际 Python 模块组合及最终 69 项新旧 Qt 离屏组合，均通过；这些组存在重叠，不相加为总数。

M4 最终实际 Go HTTP × 新 Manager 组合 **21/21 通过，56.234s，0 失败/错误/跳过，0 隔离拒绝**；与 G6 使用同一最终 exe，SHA256 为 `7AA640F7DFE006C134B20033895D026464158B84D3B9D08C2A1CD600AAB022E0`。首轮 20/21 及失败记录保留：该用例把取消且无回执误作未提交证明。仅按冻结契约校正测试，未知结果保留 checkpoint，后续显式上传也不能自动认领/重发；生产协议未放松。

入队快照/CAS、空绑定安全认领、Claim 故障调度、能力检查、来源变更定向 UI 更新和进程级保留清理六项缺陷，在上述范围内关闭。Go schema v2 的升级与整套快照回滚要求见 `task-safety-v2.md`。本轮没有生产迁移、真实模型调用、提交、推送或部署。

## 首次开发结论（历史基线）

Go 管理协议与 gemini-manager 独立接入代码已在两个正式项目落地，本轮离线/合成联合验收通过。Go 最终验收前后302个Go/SQL/模块输入的路径和SHA256一致；全仓测试、vet、Windows构建和Linux amd64交叉编译均成功。Linux交叉编译不代表运行验收。

Manager 首批9项真实Go HTTP场景通过，追加recover/manual-disable竞态/sync-expired/remote-only定向4项通过（其中remote-only复跑不重复计为独立场景），覆盖12个独立场景。15项真实Python模块组合、最终37项新旧Qt离屏组合通过；25项新后端模块与原187项Aitoapi回归共212项通过。五处M4联测缺陷全部关闭。

此结论不等于生产全面验收：Manager SQL联测使用明确受限的合成SQLite适配；真实MySQL按用户直接确认延期，macOS、Linux服务运行、真实Google/模型/两种WAA联网和生产启用未测。没有执行提交、推送、部署或生产迁移。

## 首次开发交付（历史基线）

| 任务 | 代码与模块证据 | 仍需合并的验收 |
| --- | --- | --- |
| G0 | 中立契约/OpenAPI、进程级装配、generation epoch、安全状态/用量投影；正式前端 embed 构建；302输入稳态全Go test/vet、Windows构建及Linux交叉编译通过 | 真实平台/生产未测 |
| G1 | SQLite 身份/双版本/Key 摘要/任务/幂等/审计/不可变回执/恢复；17 顶层及故障并发子用例通过；Unicode clientRef 补测通过 | 最终组合报告 |
| G2 | 原生创建/登录/配置/Cookie共同协调、文件日志、native lease；最终账号全包/app并发定向/vet通过，含认证时间栅栏和提交发布串行 | 最终组合报告；实际浏览器/WAA未测 |
| G3 | 管理 HTTP、scope/Key、任务编排/取消/回执；18 函数及8路由子测试、vet通过 | 最终组合报告 |
| G4 | 单账号/模型/Playground隔离验证、身份与终止/有效输出检查；协议合成测试通过，最新源码由最终全Go回归覆盖 | 真实Google/WAA未测 |
| G5 | Key 控制台、权限/期限/一次性令牌/撤销；27专项、lint、typecheck、临时构建通过 | 浏览器视觉/真实剪贴板/平台实机未测 |
| G6 | 真实 Go HTTP × 实际新旧 Python 客户端10项、原生集成7项通过，含真实auth-expired注入/恢复、Key/取消/崩溃屏障 | 最终组合报告 |
| M1 | 独立实例/Key/绑定/请求/证据七表、显式迁移、SQL分页/CAS；35项模块与旧迁移检查通过 | 用户确认真实隔离MySQL另行验收 |
| M2 | 复用旧传输/runner/采集/租约的窄适配、独立证据/服务/组合根；25新模块+原187项回归共212项通过；9组合根通过；Go登录过期独立分类修复并通过真实HTTP复测 | 真实MySQL延期、平台未测 |
| M3 | 实例目录/企业绑定/后台宿主；150项相关离屏/回归通过；最后显示/邮箱/管理Key门槛修复47项定向通过；M4最终37项组合通过 | macOS未测 |
| M4 | 旧187回归、15跨模块边界、最终37新旧Qt组合；真实Go→新适配器首批9项及恢复/同步等定向4项通过，12个独立场景、五缺陷全部关闭 | MySQL用户确认延期，真实平台/Google/生产未测 |

上表记录模块实际证据，不可相加为不重叠总测试数，也不表示生产通过。每个任务的实际命令和限制以其专属交付记录为准。

## 已协调修复的接口与并发事项

- Go 排空许可保持到发布，native active lease 单独证明账号/epoch，避免等待自身；BindRuntime 不阻断已在途 native 回写。
- native 提交与发布串行，发布核对当前版本；旧失败不能污染新状态。认证成功即使状态不变也维护时间栅栏，防旧失败晚到。
- 管理 HTTP 保持严格英文错误/状态、UTC毫秒、请求证明；空items优先预检、不可变回执和Playground扩展独立。
- Manager 指纹、游标粒度、解绑同次GET双版本、分页边界与pending验证终态证据已对齐。
- 新采集复用共享来源，但显式关闭旧Pro/Flash槽位写入；可用性按SQL来源版本/身份派生。旧Key/checkpoint/验证历史保持隔离。
- remote-only verify 不解密本地来源；capture/recover 在申领前解析并校验真实窗口资源。
- Go auth_required/401可以保留enabled=true，新backend独立识别登录过期并显示“登录已失效”；恢复不会覆盖较新的人工停用。管理验证只要求管理Key，独立模型Key不作为门槛。
- 带Camoufox嵌套缓存的硬删除采用同盘目录暂存与可恢复提交，最终版本由稳态全Go回归覆盖。

## 交付入口

- Go实现/命令：`deliveries/G0.md`至`deliveries/G6.md`；公共接口 `contract.md`、`g0-status.md`、`openapi.json`。
- Go跨语言验收：`acceptance.md`、`fixture.md`，可复用 `scripts/management_acceptance.py`。
- Manager正式项目：`docs/AISTUDIO2API_CONTRACT.md`、`AISTUDIO2API_DELIVERY_M1.md`至`M4.md`、`AISTUDIO2API_ACCEPTANCE.md`、`AISTUDIO2API_USAGE.md`；显式迁移版本 `20261020_01_enterprise_aistudio2api`，本轮没有执行生产迁移。

## 环境与限制

早期 Windows Application Control 曾拒绝特定 Go 测试二进制。后续正常同用户执行已通过多个包及真实HTTP，未修改/绕过策略；历史阻止与后续通过分别记录，不能泛化。

本机没有可用 mysqld/mysql；Docker Desktop 已安装但 daemon 未运行。总控尝试以隐藏方式启动现有程序，后续进程与 Linux engine pipe 仍未就绪；没有安装、拉镜像或创建容器。真实 MySQL 迁移/并发/事务不能用 Manager 合成 SQLite 的通过替代。

用户已直接确认“暂无，MySQL 验收另行安排”。本轮不再准备或连接数据库资源，真实MySQL项明确延期而非通过；显式迁移与测试入口均保留供后续隔离环境使用。

本轮不使用真实配置、账号、Cookie、Key 或模型；没有生产迁移/部署。真实 Google/两种 WAA 联网、Linux服务运行、macOS 和生产启用须在另行安排的环境验收，未测项不能标记通过。
