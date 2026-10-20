# G6 management acceptance

## 本轮 queued snapshot / Claim 修复验收

2026-10-20；总控稳态构建交接后，**22项真实HTTP×Python全部通过，7项native/schema/GoWAA-cold全部通过**，0失败/错误/跳过。G6未重编译或修改fixture/runner/业务源；本轮二进制SHA256 `7AA640F7DFE006C134B20033895D026464158B84D3B9D08C2A1CD600AAB022E0`（执行前后相同）。原有10/7历史不叠加计数。MySQL用户明确延期，本轮不准备服务。

| ID | 本轮必须证明 | 实际结果 |
| --- | --- | --- |
| Q01 | queued batch enable不能恢复其后人工停用；queued archive/reload/reload-auth同样拒绝旧版本 | PASS；4项真实HTTP/真实双版本VERSION_CONFLICT |
| Q02 | queued restore不能以执行时新snapshot覆盖较新的native restore | PASS；真实coordinator控制推进版本，旧任务VERSION_CONFLICT |
| Q03 | model/connection test在stateVersion变化前置检查失败，不进入Target.Open | PASS；2项opened/generated均只含原blocker |
| Q04 | 先queued replace推进credentialVersion，后queued test不得绑定新凭据验证 | PASS；真实verify:false替换后旧test拒绝，0额外Open/model |
| Q05 | 默认verify:true legacy replace在新manual状态后不再运行模型或写凭据 | PASS；旧任务VERSION_CONFLICT，manual状态保持 |
| Q06 | admission snapshot跨restart保留；同一原Key/idem仍回原ack，不刷新snapshot | PASS；原Key真实重放、重启target计数0 |
| Q07 | Claim事务前瞬时失败不丢queued；事务后结果丢失不重放模型 | PASS；2次before故障后自动执行1次；after故障安全interrupted且重启0model |
| Q08 | v1缺快照旧queued安全interrupted，不回填执行时版本 | G1组件PASS；已只读复核其真实v1→v2/rollback/reopen/损坏v2拒绝降级证据；总控全仓exit0 |
| Q09 | Prune定时触发、短批事务、Close退出；不误删receipt/tombstone | G1分批清理+G0独占timer/app close-order组件PASS；总控全仓maintenance PASS2.792s；非G6 HTTP证明 |

本轮实际命令：

```powershell
G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B scripts/management_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe
& C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe '-test.run=^TestManagementAcceptance(Native|FixtureRejects|Concurrent|RealHandler|GoWAA)' '-test.v' '-test.timeout=30s'
```

第一组exit0，**22项/0失败/0错误/0跳过，7.321s**；第二组exit0，**7项PASS**。日志 `runtime/management-development/g6/python-http-s2.log`、`go-integration-s2.log`，不含ready或Key。另`--check` exit0（343 refs/21 errors/httpExecuted:false）；`--list-tests` exit0（22项）；`git diff --check` exit0。没有新执行阻塞或失败，没有重新编译/移动改名exe；只在正常同用户权限流程运行总控交接的canonical二进制。

机器可读安全汇总 `runtime/management-development/g6/s2-result.json` 包含本轮hash/exit/count；源码hash核对与详细结果见 `deliveries/G6.md`，仅文档与结果元数据更新。

总控全仓证据另行复核：`s2-closing-result.json`为Tests/Vet/WindowsBuild/LinuxCrossBuild/FixtureBuild均0，SourceStable=true、322输入一致；`s2-all-tests.log`中maintenance实际PASS2.792s、app PASS9.427s、store PASS3.845s。G6没有重复全仓验收，也不把该组件证据冒称G6自己的HTTP断言。源码保持稳态，仅本记录/操作文档更新。MySQL、真实Google/生产、macOS/实际Camoufox、Linux运行未执行。

## 前一轮验收记录（历史）

日期：2026-10-20。G6 当前交付：**10 项真实 Go HTTP × 实际 Python 新旧 client/readback 通过，7 项独立 Go 集成测试通过**。均 0 失败/错误/跳过。完整项目最终生产装配、Manager repo/UI/MySQL 仍由总控/M4联合验收；冻结契约与本测试fixture不代表生产已启用。

依据：`parallel-tasks.md`、`contract.md`、`openapi.json`、`g0-status.md`、`baseline.md` 与评估报告。G6 只改独立测试/fixture/runner 和本操作文档；业务缺陷交总控协调原 owner。共享目录保留他人改动，不提交、推送、部署、生产迁移、真实配置读取或真实模型调用。无 Actions/外部审核。

## 验收层次与矩阵

PASS 仅指实际执行并通过的层次。PREPARED 是源码/场景具备，未运行；PENDING 是模块/场景尚待交付；BLOCKED 是实际命令被环境阻止。编译、vet、schema、模拟上游 observation、真实 handler HTTP、真实 Google、生产分别记录。

| ID | 必须证明 | 真实组件/证据 | 状态 |
| --- | --- | --- | --- |
| C01 | 21 个旧 Python 错误 status/message 与 Go/OpenAPI一致；内部 refs；Playground闭合扩展 | AST读取旧实际 client；OpenAPI 343 refs；独立闭合 schema | PASS：source-check-only，非HTTP |
| H01 | 空 accounts/tasks 为 `[]`；readiness false 仍200；preflight空items不入队 | actual AitoapiManagementClient → real Go handler/store | PASS（真实HTTP） |
| H02 | requestId/header/no-store/Content-Type；404/405/Allow；畸形JSON/未知字段/非法分页/认证 | real Go router/auth/parser；真实响应用冻结OpenAPI schema验证 | PASS |
| H03 | import→task→account→不可变receipt；verify:false零generation | real coordinator/file participant/SQLite/task service；target计数 | PASS |
| H04 | 同幂等内容回放原queued ack；异文可信409且不是not-admitted | actual Python client + real admission/store | PASS |
| H05 | 同邮箱重复导入拒绝；原UUID/index replace；archive/restore→fresh CAS；启停/旧CAS拒绝 | real coordinator/HTTP/tasks | PASS |
| H06 | lost-ack后仅GET双扫描task/readback→原receipt | actual requests response丢弃 + actual AitoapiTaskReadback | PASS；实际只读methods断言 |
| H07 | wrongacct/Build/empty/thought/finish-only/missingterminal/wrongmodel/identity/timeout拒绝；OK固定Playground | real verifier + fake upstream observation | PASS（verifier observation层）；原始事件解析由G4实际target单测验收 |
| H08 | cancel/crash/restart不重复generation/upload；running→interrupted；历史receipt保留 | real process kill/reopen/recover；真实SQLite/账号文件 | PASS；重启计数从0开始且任务不重放 |
| H09 | 新Python client/readback保留actualChannel与版本；新旧并存 | actual M2 client/readback直接真实HTTP；M4更完整业务层独立 | PASS（client/readback层），M4 repo/UI/MySQL另列 |
| H10 | 硬删除/新index不复用、Key scopes/revoke/queued取消 | real native delete/file participant/store；real console session/Key HTTP | PASS |
| H11 | 登录过期提交→真实GET新CAS→replace恢复；原ID/index；原receipt；晚到manual-disable不被覆盖；0生成 | test-only auth-expired经real coordinator/store/files提交，actual Pythonclient真实HTTP | PASS；M4 actual Service/Repo/capture source版本另行验收 |
| R01 | 文件写前/后、DB提交/receipt后各崩溃边界；较新人工停用保留 | real participant decorator 5屏障 + kill/restart；实际2个active native lease响应交错 | PASS；SQLite元数据/receipt同一事务原子提交，不伪造其中间态 |
| R02 | 已持有native lease写入不等待自己、epoch固定/失效拒绝、并发Set-Cookie/管理CAS | actual native AccountLease/hooks/coordinator/G0 runtime adapter | PASS；真实管理drain等待native时native仍可提交；管理旧CAS拒绝 |
| N01 | 原生console/login/公开API/WAA装配回归 | real root/router/session；原生model auth/空models；Go WAA冷装配不定位浏览器 | PASS（所列范围）；完整公开生成协议/Camoufox现场由总控/原测试单列 |
| B01 | 新Go fixture/app测试编译及vet | 共享GOCACHE/GOTMPDIR，-p 2 | PASS；首轮并发unused import已随G4交付消除 |
| B02 | Windows新测试exe执行 | 正常同用户上下文，无策略变更 | PASS（本次G6 app/fixture）；历史api.exe拦截不泛化 |
| X01 | Linux编译/运行 | 交叉编译与实际Linux执行分别记录 | NOT_EXECUTED |
| X02 | macOS/Qt/MySQL/实际Google/生产 | M4/单独现场授权 | NOT_EXECUTED；本轮不作通过声明 |

## 实际命令

- `python -B scripts/management_acceptance.py --manager-root G:/code/gemini30/gemini-manager --check`：exit 0；343 OpenAPI refs、21 legacy error pairs、closed Playground扩展检查通过；`httpExecuted:false`。
- `gofmt -w internal/app/management_acceptance_fixture_test.go`：exit 0，仅G6文件。
- `git diff --check`：exit 0（当时工作树）。
- 共享缓存环境的 `go test -p 2 -c -o C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe ./internal/app`：exit 1，`internal/app/management_verification.go:10:2: "net/http" imported and not used`。G4文件正在开发，G6未跨改；此命令未生成可用验收证据。
- 模块交付后同一fixture编译：exit 0。含auth-expired的最终SHA256：`CA9680A3E1A7C6290C3F0D0AB9FDC51919C2E2D1C7973A16A650E1A7C9C4379B`。
- `g6-app.test.exe '-test.run=^TestManagementAcceptance(Native|FixtureRejects|Concurrent|RealHandler|GoWAA)' '-test.v' '-test.timeout=30s'`：exit 0，7项Go集成测试通过，未运行opt-in server测试。可在项目根或包目录运行，schema测试兼容两种位置。
- `G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B scripts/management_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe`：最终exit 0；**10项**，0失败/错误/跳过，3.486s。上游fake，五模块和Python transport/client/readback真实；测试用runtime/projection明确合成，不读取生产配置。
- `go vet -p 2 ./internal/app`：exit 0，使用共享缓存。
- M4报告Manager scripts/secrets.py遮蔽stdlib secrets后，driver移除该import，用os.urandom(16).hex生成nonce；将Manager/scripts置于sys.path首位的importlib导入+真实fixture启停定向检查exit0。未为此重跑无关业务套件。

最终运行日志：`runtime/management-development/g6/python-http-final.log`、`go-integration-final.log`、`driver-import-final.log`；仅测试名称、计数/安全诊断，不含ready或合成Key。

早期fixture停止回包曾有WinError10054：首次7项有2个cleanup错误；仅Flush修复仍有chunked尾块race。最终版本读完stop请求体、明确Content-Length、完整Flush后发stop信号，main有界Shutdown。另一次9项复测的cross-site Key拒绝请求出现单次transport reset（非shutdown、0业务断言失败）；新增非secret fixture诊断后定向用例exit0，最终完整9项exit0；保留此历史，不宣称长期网络稳定性。初次Go schema测试从项目根直接执行时相对路径错误已修复，最终7项全过。

操作协议/启动停止/临时目录/合成数据与M4复用入口见 `fixture.md`。本次没有Application Control执行阻止；未改变系统策略，没有复制改名/换宿主绕过。更完整Manager实际仓储/MySQL/Qt验收及最终生产装配由总控/M4继续；Linux编译/运行、macOS、真实Google、生产均未执行。
