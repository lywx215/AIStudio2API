# AIStudio2API 管理协议并行开发任务

## 执行约定

- 正式目录：`G:\code\gemini30\AIStudio2API`。同一 Codex 应用中的七个任务窗口，共用当前工作树；不新建分支或 worktree。
- 用户指定所有新建开发窗口使用 GPT-6.1 Sol（`gpt-6.1-sol`），推理强度极高（`xhigh`）；后续 Manager 窗口沿用。
- 本对话负责向本批新建窗口发送任务、依赖交接和返工消息，并验收结果。各开发窗口不得自行创建其他开发会话或派发额外开发代理。
- 首期为企业账号完整闭环，AIStudio2API 与旧 Aitoapi 绑定并存，独立模型验证固定 Playground。用户于 2026-10-20 明确要求加速：Go 与 Manager 两条开发线并行，不再等待 Go 全部验收后才开发 Manager。
- 以 `docs/gemini-manager-integration-assessment-20261020.md` 及 Aitoapi 的管理契约/OpenAPI为依据。Aitoapi-custom 为只读参照；Manager 在其正式项目独立开发。
- 不提交、推送、部署、访问真实配置/凭据、执行生产迁移或调用真实模型；不使用 GitHub Actions，不启动 Claude/Antigravity 审核。
- 保留原有未提交修改。只编辑分配文件；公共接口变更提交 G0 协调。不要格式化、删除或覆盖其他窗口文件。
- 网络/浏览器/模型等待不能进入数据库事务。测试只用临时目录、合成账号与假上游；不得用空实现或 mock HTTP 成功冒充真实模块联测。
- 每个窗口维护自己的 `docs/management/deliveries/Gn.md`，记录接口、文件、命令、实际结果和剩余项。不得编辑本任务台账或其他窗口交付记录。
- 全局构建/测试由调度窗口协调，避免重复冷编译和磁盘缓存竞用；各窗口可以运行其拥有模块的必要检查。
- G 盘当前仅约2 GiB空闲。Go构建缓存统一使用 `C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-build`；不在G盘创建独立大缓存。测试并行编译建议 `-p 2`，未经协调不重复安装依赖或全量冷编译。

## 文件唯一负责人

| 窗口 | 所有权 | 禁止跨界 |
|---|---|---|
| G0 协议与集成 | 中立公共契约包；docs/management/contract.md、openapi.json、g0-status.md；internal/api/router.go；internal/app/app.go、lifecycle.go、runtime.go；依赖清单；最终生产装配/安全投影与其辅助文件；internal/requestdb/management.go 安全用量 SQL | 不接管其他窗口的存储/HTTP/验证器/前端实现；必要更改先调度交接 |
| G1 管理存储与恢复 | 新增管理存储包、SQL schema、UUID/index、版本、Key 摘要、任务/幂等/审计/回执/恢复记录及其测试 | 不修改账号文件、运行池、公共契约或根路由 |
| G2 账号写入与生命周期 | internal/aistudio/accounts.go、auth.go；internal/app/admin.go、auth_retry.go；新增账号协调/恢复参与者及其测试 | 不修改 G0 生命周期/runtime 或 G1 schema |
| G3 管理 HTTP 与任务编排 | 新增 management HTTP/auth/任务服务文件及测试；Key 管理路由；只读安全状态投影 | 不修改 router.go、原生 admin_auth.go、G1 schema 或 G2 账号实现 |
| G4 Playground 定向验证 | 新增验证包、独立 app 验证适配文件与测试 | 不修改 runtime.go/accounts.go；通过 G0/G2 交接公共钩子 |
| G5 管理 Key 控制台 | 本批 Vue/TypeScript 前端改动与前端针对性测试 | 不修改 Go、公共协议、package.json/lockfile；依赖变更交 G0 |
| G6 跨语言契约与回归 | 独立集成测试/fixture/运行器；docs/management/acceptance.md、操作说明 | 不修改任何生产业务文件或 G0 契约；缺陷交原 owner |

目录分区和精确构造接口由 G0 在冻结包中补充。冻结后新文件也必须按模块 owner 归属，不能仅因为文件尚不存在就跨界创建。

## G0 首阶段冻结要求

1. 中立契约无循环依赖；明确 Store、AccountCoordinator、TaskService、Verifier、HTTP registration 的构造和调用接口。
2. 管理 Key、任务、幂等、审计、版本与回执为进程级，不随 StartService 重建 generation。
3. 独立 `/api/manage/v1` 根挂载，避免落入控制台 Session/HTML/redirect；JSON 404/405由命名空间终结。
4. UUID accountId、永不复用 index、原生邮箱映射；状态完整返回 enabled/archived/expired/disabledReason/disabledStatus/disabledAt 和双版本。
5. 成功/错误/202与 Aitoapi 客户端一致；错误英文 message、状态、req_UUIDv4、X-Request-Id、Cache-Control严格一致。所有时间固定 UTC 毫秒。
6. 空 items 预检在入队前返回 INVALID_REQUEST；不因缺少 Idempotency-Key 改变旧客户端预检结果。
7. 任务 kind、clientRef、createdByKeyId、上传回执、幂等重放和取消/重启语义；已有提交回执不能从当前账号状态伪造。
8. 文件与 SQLite 的可恢复提交顺序；恢复在生产账号发布前完成，不重放任何模型动作。
9. 凭据自动刷新、原生 UI/重新登录/配对/Chrome 导入参与同一版本与写入协调；瞬时 busy/cooldown不冒充登录过期。
10. Playground 成功扩展 `actualChannel:"playground"` 有独立 schema，不能直接添加到原 OpenAPI 的 additionalProperties:false 对象而不更新 Go schema。
11. Key 创建和撤销需要真正控制台认证；关闭登录时的 loopback 路径不得意外获得 Key 管理权限。
12. 设置/用量/诊断按 Go 实际能力提供安全投影，禁止将 Key、Cookie或代理密码放入DTO/日志。

冻结完成后，G0 在 g0-status.md 明确写入 `CONTRACT_FROZEN`，列出接口文件与测试命令；先交回调度，不自行启动 G1–G6。

## 调度顺序

1. G0 首先执行契约冻结，保留最后生产装配任务。
2. 冻结通过后并行启动 G1/G2/G3；允许依赖假实现进行各自模块开发。
3. 为响应加速要求，契约冻结后 G4/G5/G6 也立即并行：定向验证和前端使用冻结接口开发，G6先准备fixture/验收矩阵；真实组合验收等待G1–G4交付。G0统一装配真实依赖。
4. 与 Go 并行启动 Manager M1/M2；M2 优先冻结仓储与 UI 服务接口，M1 可先实现独立迁移。M3/M4 可先实现 UI 与验收准备，真实组合验收等待依赖交付。
5. Manager 四个窗口均归属其正式项目。继续遵循原项目 AGENTS、配置/密文边界及显式迁移规则；开发并行不等于跳过联合验收或生产启用。

## Manager 文件唯一负责人

| 窗口 | 所有权 |
| --- | --- |
| M1 数据迁移与仓储 | enterprise/aistudio2api_migration.py、aistudio2api_repository.py；database_migrations.py 的新增迁移注册；本模块测试；docs/AISTUDIO2API_DELIVERY_M1.md |
| M2 业务适配与组件集成 | enterprise/aistudio2api_contract.py 与 docs/AISTUDIO2API_CONTRACT.md；新增非 UI 适配/runner/readback/evidence/service；enterprise/components.py；必要的旧 Aitoapi 后端兼容抽取；docs/LLM_PROJECT_GUIDE.md 与本模块测试；docs/AISTUDIO2API_DELIVERY_M2.md |
| M3 企业界面与后台宿主 | 本批 ui/ 下新增和现有 Qt 页面、后台 host；必要 GUI 入口；对应 UI 测试；docs/AISTUDIO2API_DELIVERY_M3.md |
| M4 双后端联合验收 | 跨模块测试、合成 fixture、验收运行器；docs/AISTUDIO2API_ACCEPTANCE.md、AISTUDIO2API_USAGE.md；docs/AISTUDIO2API_DELIVERY_M4.md。不得修改其他窗口业务文件 |

Manager 全部保留旧 Aitoapi 类/签名/默认模型和绑定行为。新实例配置、绑定、检查点、验证证据独立于 Pro/Flash 旧槽位；来源凭据、密文机制及租约流程复用。模型来自每实例配置，验证通道固定 Playground。

## 验收门槛

- import → task → account → 不可变上传回执；同邮箱replace；archive/restore后取新版本再replace。
- 双CAS、幂等同文/异文、响应丢失只读定位；分页/排序/时间精度/错误证明与真实Python客户端联测。
- 文件/数据库/回执提交各边界崩溃、取消、服务启停重建；不重放写入或模型动作，不恢复较新的人工停用。
- verify:false零生成调用；定向Playground不换号、不转Build；空回复/仅思考/缺结束/超时/取消不成功。
- 原生控制台、公开协议、登录、Cookie刷新和两种WAA装配回归。
- Manager阶段保持当前187项Aitoapi回归，验证新操作不写旧槽位/密文/检查点/evidence；隔离MySQL迁移/CAS/分页与Qt后台验收。
- 实际未完成的Google、Linux、macOS或生产验收分别列明，不用离线通过替代实测。

## 当前状态

- 调度：G0 契约已冻结并进入装配；G1–G6 与 Manager M1–M4 全部创建并并行推进。独立实现提前展开，真实组合验收等待依赖交付。
- 开发窗口统一使用 GPT-6.1 Sol / xhigh（极高）；共用正式目录，不创建分支或 worktree。
- 2026-10-20 本轮基线：7 个既有 Go 包通过；`internal/api` 测试二进制被 Windows Application Control 阻止启动，未执行，不能记为通过。前端 lint、typecheck 通过。命令和日志见 `baseline.md`。
- 初始提交：00ad27b5947b0feb2f7fc4dde0c0853ac2a54200。
- 初始业务代码干净；保留既有未跟踪评估报告。
- 本轮收敛：两个正式项目代码与离线/合成联合验收完成，具体通过证据及边界见 `integration-status.md`；最终Go源码稳态校验302输入一致。Manager真实MySQL用户已直接确认另行验收，真实平台/Google/生产未测，未提交推送部署。
