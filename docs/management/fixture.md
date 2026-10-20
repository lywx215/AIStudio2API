# G6 synthetic HTTP fixture protocol

**当前canonical为selectable channels v3**：`C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe`，SHA256 **6752A62C86B42474CC609BB90F9928817054CFB620F377A8D61E29F088B5D7F6**。G0拥有本轮Go/fixture；G6未改Go或重编译。顺序严格实际复验V08/V11 2项→完整通道12项→旧HTTP22项全部exit0，0失败/错误/跳过；DEF-G6-CHANNEL-001已关闭，索引校验未放宽。17个相关Manager/driver/exe指纹前后相同。

该v3产物基于总控测试源快照 `C:\Users\lywx2\AppData\Local\Temp\aistudio2api-channel-v3-source-eac5d9da27`（非branch/worktree），331本批冻结输入一致、前后0变化；G6在该目录调用正式项目绝对路径Python driver，不重编译或读取真实配置。精确命令与分层证据见 `channel-acceptance.md` / `deliveries/G6.md` 的v3节；本次日志 `channel-http-v3-targeted.log`、`channel-http-v3-final.log`、`python-http-selectable-v3-final.log`，安全总表 `channel-v3-summary.json`。

v3保持g6-fixture-v1启停/合成Key/显式临时目录协议，新增通道按请求计数/events与g6-playground-fail/g6-wrongchannel/g6-cancel-after-build等仅测试fake-upstream场景；默认Build、显式PG或双选均由真实handler/task/store/coordinator/verifier执行。V10完全丢失模型ack仍不唯一定位，只读保留unknown，不重发/重放；fixture计数不是归属证据。真实Google/模型/生产/MySQL均未执行。**下面S2与早期hash记录仅属历史，当前hash以上方v3为准**。

本轮queued snapshot/Claim修复已完成真实验收：总控稳态构建的新canonical fixture运行**22项HTTP/Python+7项native/schema/冷WAA全部通过**，均0失败/错误/跳过。G6没有重编译或修改fixture/runner/业务源码。**当前exe SHA256 `7AA640F7DFE006C134B20033895D026464158B84D3B9D08C2A1CD600AAB022E0`**，路径仍为 `C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe`（总控构建，UTCmtime2026-10-20 06:17:35，61849088字节；执行前后哈希一致）。下方CA9680...旧exe不含新控制，只是历史，不能代替本轮证据。

实际完整命令、日志和组件分层见 `deliveries/G6.md` / `acceptance.md`：Python22项7.321s；Go7项；`runtime/management-development/g6/python-http-s2.log`、`go-integration-s2.log`。MySQL按用户要求延期，未准备服务。所有Key仍为本次合成、临时数据、loopback fake上游，无真实Google/模型调用。

新控制仍只存在`internal/app/management_acceptance_fixture_test.go`，需本次合成admin Bearer：

- `POST /__g6__/native-restore {"accountId":"<synthetic UUID>"}`：读取真实当前账号，仅fixture@example.invalid可选；调用真实coordinator.Restore并带当前双CAS，返回`{account:<实际提交安全DTO>}`。用于queued restore期间的原生并发变更，不假造GET/任务成功。
- `POST /__g6__/claim-fault {"mode":"before"|"after","count":1..3}`：一次armed序列；before在真实ClaimTask之前返回PersistenceError、DB仍queued；after实际ClaimTask成功提交running后丢弃返回值，worker拿不到private input。控制不改row/任务终态/receipt，不伪造model成功。真实state计数新增claimAttempts/claimFaults/claimFaultRemaining。
- driver复用`native_restore(account_id)`、`claim_fault(mode,count)`，`--list-tests`只列准备用例不启动fixture；`--test <method>`可定向运行。restart/idem场景必须保留原合成Key：重启ready新Key是另一namespace，不能拿它冒充原idem重放。

以下为前一轮已执行协议/二进制历史及仍适用的启动操作；当前构建hash和结果以上方S2记录为准。

2026-10-20。G6 独占测试文件/runner；实际执行结果见 `deliveries/G6.md`。协议名 `g6-fixture-v1`。不创建生产配置，不连接 Google，不读取真实账号/Key。

前轮M4交接二进制（历史）：同路径SHA256 **CA9680A3E1A7C6290C3F0D0AB9FDC51919C2E2D1C7973A16A650E1A7C9C4379B**。该版本新增下述auth-expired控制，**10项Python/7项Go及vet曾通过**。当前canonical已由上方新hash替代。stop请求未读完和chunked尾块race已修复：显式Content-Length、Connection close、完整确认Flush后才发stop信号；main有界server.Shutdown，不在handler内等待自身关闭。仅Flush的早期版本仍有WinError10054，不可用于最终验收。

**M4 recover场景已可接入**：`POST /__g6__/auth-expired {"accountId":"<已导入合成UUID>"}`，Authorization 为本次admin合成Key。返回HTTP200 `{"account":<真实已提交Account安全DTO>}`，其中expired=true/disabledReason=auth_required/disabledStatus=401/disabledAt为UTC毫秒；ID/index/credentialVersion保持，真实stateVersion递增。真实 coordinator.CommitNative → 实际SQLite Store → 实际AccountFileParticipant → runtime publication；未伪造GET响应。只接受本fixture已存在的@example.invalid账号，不接受调用方状态/版本/文件/凭据字段；manual-disabled、非enabled或archived返回409 INVALID_STATE，避免覆盖较新人工停用。后续请真实GET获取双版本再replace。Python复用方法 `fixture.auth_expired(account_id)`。此控制仅_test.go，生产不存在。

## 给总控与 M4 的交接

源码：`internal/app/management_acceptance_fixture_test.go`；驱动和旧实际 Python 客户端验收：`scripts/management_acceptance.py`。fixture 使用真实 `managementstore.Open` → 包内 `newManagementAccountCoordinator` → `Recover`/`BindRuntime` → `managementverify.New` → `management.NewTaskService`/`Start` → `management.NewHTTP` → 原生 `api.NewHandler`/`rootHandler`。它不需要额外生产构造 hook。合成运行时仅实现无浏览器 publication lease；安全状态 projection 为明确的 synthetic/stopped。Google/Playground target 合成，真实 verifier、文件 participant、SQLite、任务与 HTTP/auth 均参与。

可在正常同用户宿主上下文编译：

```powershell
$env:GOCACHE='C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-build'
$env:GOTMPDIR='C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp'
go test -p 2 -c -o C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe ./internal/app
G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B scripts/management_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe
```

编译成功不等于执行通过。Windows Application Control 若拒绝 exe 启动，记录 BLOCKED；不换宿主、改策略或复制改名绕过。系统策略阻止后无需逐例重复启动。

M4 可以用 `importlib.util.spec_from_file_location` 导入此脚本，复用 `FixtureProcess(executable).start()`；字段 `origin`、`root`、`token("admin"|"operator"|"readonly")`、`ready["keys"][template]["keyId"]`；方法 `wire`、`poll`、`metrics`、`barrier`、`wait_barrier`、`release`、`auth_expired`、`restart(crash=True)`、`close`。Driver用os.urandom生成nonce，已消除Manager scripts/secrets.py遮蔽问题并定向验证，无需RNG兼容shim。每例新建FixtureProcess即fresh state，不提供覆盖已有状态的reset路由。Driver 创建 `aistudio2api-g6-<random>` 子目录和 nonce marker，环境变量只含路径/nonce，Key 由真实 store 创建并在进程内流转。M4 的 audit guard 若将 `TMP/TEMP/TMPDIR` 和 `tempfile.tempdir` 指向本次 `runtime/devices/...`，同一 driver 会在该显式临时根下创建 fixture，Go 的 `os.TempDir()` 必须得到同一路径。既有真实目录不能充当 fixture 根。

独立启动协议（driver 已负责设置）：

- `G6_MANAGEMENT_FIXTURE=1`：显式启用；普通 `go test` 跳过 fixture server。
- `G6_MANAGEMENT_ROOT`：绝对路径，必须为 OS temp 的子目录、目录名以 `aistudio2api-g6-` 开头，目录树无 symlink。
- `G6_MANAGEMENT_NONCE`：32 字符随机 nonce；根下 `.g6-fixture` 必须完全匹配。
- 命令参数：`-test.run=^TestManagementAcceptanceFixtureProcess$ -test.v -test.timeout=16m`。进程最长存活 15 分钟。
- 显式文件：`management.sqlite`、`synthetic-auth/`、`synthetic-journal/`；均位于本次根下。重启复用根与 marker；从真实协调器恢复开始。driver 只清理自己创建并验证 marker 的临时根。
- ready：stdout 单行 `G6_READY {json}`，含 `protocol:"g6-fixture-v1"`、`syntheticOnly:true`、随机 `origin:"http://127.0.0.1:<port>"`、`keys:{admin|operator|readonly:{token,keyId}}`。同一JSON以0600临时文件原子rename至本fixture根的 `ready.json`。其余 Go test 输出不作为 ready。ready 含本次合成 Key，不持久化到报告，不放命令行。
- shutdown：`POST /__g6__/stop {}`，Bearer 为本次 admin 合成 Key；随后等待进程 exit 0。`FixtureProcess.stop(crash=True)` 对本次子进程执行 kill，用于恢复场景。正常退出关闭 server→tasks→verifier→store。

测试控制命名空间 `/__g6__/` 只存在于这个 `_test.go`，生产二进制中不存在。`GET /__g6__/state` 受本次 admin 合成 Key 保护，提供fake-target实际Open/Run/Close计数 `opened/generated/closed`，别名 `generationCalls`，fake-target场景的 `playgroundCalls/buildCalls`；这些是合成target调用，不是实际Google/Build transport调用。上传/同步/readback 的 `generated` 必须保持 0；`g6-ok` model probe 每次加 1。真实store的测试decorator计数 `admittedTasks`（不计幂等重放）、`mutations`（成功返回durable account的commit按kind计数），不会预置成功计数。重启计数归零，持久任务/回执通过真实管理 GET 验证，不从计数推断。

提交屏障：`POST /__g6__/barrier {"point":"before-prepare"|"after-prepare"|"after-apply"|"before-finalize"|"after-finalize"}`，一次仅能arm一个。真实文件participant方法相应位置到达后 `state.barrierReached` 返回point；`POST /__g6__/release {}`释放，或driver.kill/restart模拟崩溃。`before-finalize`/`after-finalize`位于SQLite account+原receipt原子提交后，恢复须保留提交；其余位置恢复回滚。barrier为test-only participant decorator，不访问G1私有hook、不伪造row/receipt、不持有SQLite事务等待。取消在real task cancel接口执行，提交前/后各断言已包含。

合成凭据由 driver `synthetic_credentials()` 创建：邮箱 `g6-<label>@example.invalid`、三类签名 Cookie、值为 `g6-synthetic-*`、空 origins。fixture 拒绝可识别的真实凭据；结构错误交给真实 HTTP parser。它不向外部发送任何 Cookie。模型场景由固定合成 model ID 选择：`g6-ok`、`g6-wrongacct`、`g6-buildonly`、`g6-empty`、`g6-thoughtonly`、`g6-finishonly`、`g6-missingterminal`、`g6-wrongmodel`、`g6-unconfirmed`、`g6-timeout`、`g6-cancel`。这些仅提供 verifier 边界 observation；thinking/terminal 原始事件解析仍需 G4 实际 target adapter 单测，不把 observation fake 当真实 Google 证明。

lost-ack 使用实际 requests transport：真实 POST 收到 Go 响应后丢弃该响应并抛出 transport fault；后续实际 Aitoapi readback 仅 GET 定位。未伪造 HTTP 202/账号/回执。仅丢响应是 transport fault，不能证明提交失败。

## 验收边界

提交屏障、mutation/admission计数、硬删除、控制台Key/revoke/queued取消、原生actual active lease self-drain/epoch/并发Set-Cookie、auth-expired恢复均已实际执行。完整矩阵/历史失败/最终日志见 `acceptance.md` / `deliveries/G6.md`。fixture使用合成无浏览器runtime和安全projection，独立Go测试另验证真实G0 runtime adapter。此交付不启用生产runtimeManager、不连接真实Google，不声明Linux/macOS/共享MySQL/Qt/真实Camoufox现场通过；M4的完整Service/Repo/UI/DB组合与总控最终装配独立验收。
