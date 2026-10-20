# 六项缺陷并发修复与验收

2026-10-20：用户在只读检查后确认修复方案，并明确要求“并发实施”。本记录由总控维护。当前状态：**ACCEPTED_OFFLINE_SYNTHETIC**，六项修复的离线/合成联合验收通过；真实 MySQL、平台实机与生产范围另行验收。

## 固定约束

- 两个正式目录共用现有工作树及窗口；不创建分支/worktree，不覆盖已有未提交修改。
- 不提交、推送、部署、执行生产迁移、访问真实配置/凭据或调用真实模型；不使用 GitHub Actions/Claude/Antigravity。
- 用户选择：仅中断缺必要版本保护的旧 queued 任务，保留安全可恢复任务、任务/幂等历史及已提交回执。
- Go HTTP 请求格式、错误封装及旧 Aitoapi 接口保持兼容。服务端入队快照不进入正文指纹。
- 真实 MySQL 按用户安排延期，macOS、真实 Google/Playground/WAA 及生产另行验收。

## 任务与唯一负责人

| 编号 | 修复内容 | owner | 当前状态 |
|---|---|---|---|
| R1 / P1 | 入队固定账号集合/双版本，旧任务不得覆盖后续人工状态；旧任务升级安全中断 | G0 契约与装配、G1 存储、G3 执行，G6 联测 | 模块/全仓/真实 HTTP 通过 |
| R2 / P2 | 首次失败空绑定经明确上传安全认领同邮箱远端，原子准备请求 | M1 仓储、M2 新后端适配，M4 联测 | 模块/真实 HTTP 组合通过 |
| R3 / P2 | 持久 queued 调度、Claim 故障读回与退避，无业务动作重放 | G0 契约、G1 存储、G3 调度 | 模块/全仓/真实 HTTP 通过 |
| R4 / P2 | 后端类型/1.0.0/稳定 ID/双 CAS/回执/Playground 能力校验 | M2，M4 联测 | 模块/真实 HTTP 组合通过 |
| R5 / P2 | 采集保存后来源变更广播，可见行后台定向读回与迟到结果保护 | M2 服务、M3 UI，M4 联测 | 模块/实际服务与仓储/Qt 组合通过 |
| R6 / P2 | 进程级启动及每小时清理，30 天保留、受控批次、退出等待 | G0 维护生命周期、G1 存储清理 | 模块/应用生命周期/全仓通过 |

原 `parallel-tasks.md` 中 ownership 继续有效。G2/G4/G5 本轮没有新增业务修改分配；发现共享原生边界缺口由总控另行交接。

## 初始复现证据

- R1：当前真实 Go handler/SQLite/协调器/任务服务及假上游：人工停用后 `enabled=false,stateVersion=2`，先前 queued enable 后 `succeeded,enabled=true,stateVersion=3`。
- R2：合成仓储夹具：已有 remote ID 为空的失败绑定，prepare replace 抛出 `AitoapiStateConflict: AIStudio2API request cannot change remote ownership`，事务回滚且无新请求。
- R5：Qt 离屏既有假服务：capture 返回 source version 3，当前行仍为 source version 2 / available。
- 修复前只读复跑：Manager 新相关 56 项、旧 Aitoapi 187 项通过；Go management/store/verifier/app 四包通过（前三包缓存）。这些结果不覆盖上述缺陷，不作为修复验收。

## 验收门槛

1. 每项新增针对性测试应先覆盖原失败条件，完成后记录实际结果，不以假协作者测试代替真实组合。
2. Go v1→v2 临时库升级可重复，缺快照旧任务不会重新取新版本执行；原 receipt/tombstone 不变。
3. 首次 Claim 失败及模糊提交经过读回，不永久遗留 queued，不重放已开始的上传/模型。
4. 空绑定认领不越过 pending checkpoint/未决请求/已有归属；并发唯一性与 CAS 保留。
5. 能力错误阻止新增远端动作；已有未知请求保留证据。预检仍零任务、零真实模型。
6. capture 已保存但 cleanup 失败也更新来源；跨视图及迟到分页不能恢复旧 available。
7. 30 天边界/清理失败/活动任务不删/维护退出经过测试；过期任务找不到不构成重发许可。
8. 最终 Go 全量 test/vet、真实 Go HTTP × Python、旧 187 项和相关新旧 Qt/契约/迁移回归分别记录。

## 交付

各 owner 在原 delivery 文件追加本轮变更、接口、命令、实际结果和未完成项；总控审查代码和集成后更新本表及 `integration-status.md`。真实 MySQL 等延期项不能写为通过。

## 总控稳态 Go 检查

本轮代码收敛后，在正常同用户执行上下文使用既定 C 盘 GOCACHE/GOTMPDIR、`-p 2`：

- `go test -p 2 ./... -count=1`：exit 0；包括 maintenance 包实际 PASS 2.792s。G0 早期特定测试启动被 Application Control 拦截的记录仍保留；本轮没有更改策略或使用替代执行方式。
- `go vet -p 2 ./...`：exit 0。
- Windows CLI build、Linux amd64 CGO-disabled cross-build：均 exit 0；交叉编译不代表 Linux 运行验收。
- `go test -p 2 -c -o <GOTMPDIR>/g6-app.test.exe ./internal/app`：exit 0，新 exe 已交 G6/M4，随后真实组合结果另列。
- 全部 322 个 Go/SQL/模块输入在检查前后路径及 SHA256 一致，`SourceStable=true`。

证据位于 `C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/` 的 `s2-all-tests.log`、`s2-vet.log`、`s2-*-build.log`、`s2-source-before.json`、`s2-source-after.json`、`s2-closing-result.json`。所有执行均未读取真实配置或调用真实模型。

总控复核另补两项边界：R5 同 source epoch 下较旧 revision9 查询不得覆盖 revision11 delta（先复现、M3 修复后 57 项相关 Qt 检查通过）；R3 Claim 成功到执行前的明确 HTTP cancel 保持 CANCELLED，不误报 INTERRUPTED（新增 target PASS，并由上述最终全仓覆盖）。

## 联合验收前提校正

M4 首轮最终 Go HTTP 组合运行 21 项，其中 20 项通过、1 项失败；原始结果保留于 Manager `runtime/devices/aistudio2api-acceptance/m4-corrective-new-go-final-20261020/go-http/`。该失败用例曾假定取消且无上传回执即可清除 checkpoint。诊断还发现其屏障路径实际返回 PERSISTENCE_ERROR，不能作为明确未提交证明。

总控与 M2 复核冻结契约：取消不撤销已提交动作，缺失回执表示结果未知；即使夹具知道任务曾处于 queued，Manager 也不能把内部屏障信息当作公开的未提交证明。取消/持久化未知的请求须保持 checkpoint 和 needs_reconcile，后续显式 upload 只能核对原任务，不能自动认领或重发。可信 pre-admission INVALID_REQUEST 拒绝后清除请求检查点的空绑定认领正向例继续保留。

本次只校正 M4 测试及说明，未放松生产行为或修改旧 Aitoapi 协议；完整修正后组合结果见下表。管理端能力校验失败同样不能证明此前请求未受理。

## 存储升级与恢复边界

Go 管理存储 schema 升至 v2，HTTP 协议仍为 1.0.0；详细公共契约及旧任务判定见 [task-safety-v2.md](task-safety-v2.md)。仅真实 v1 升级写入旧队列来源标记；损坏的新 v2 快照不能降级套用旧任务豁免。幂等原始正文/指纹/受理结果与已提交回执保持不变。

未来生产升级应停服务后对数据库、账号文件和提交日志作一致备份；回滚须恢复同一整套快照及匹配二进制，不能只恢复数据库或手动降低 schema 版本。本轮没有访问生产数据或执行该升级。Manager 显式迁移入口沿用首次实现，本次修复没有新增 MySQL DDL；真实 MySQL 事务、锁、分页和迁移仍未验收。

## 最终验收结果

| 检查 | 实际结果 | 证据/范围 |
|---|---|---|
| Go 全仓 test / vet | 全部通过，exit 0 | 322 个输入前后与收尾复核一致；见上方稳态检查 |
| Windows CLI / Linux amd64 build | 均通过，exit 0 | Linux 仅交叉编译，无服务运行验收 |
| G6 实际 Go HTTP × 新旧 Python 客户端 | 22/22，0 失败/错误/跳过，7.321s | `runtime/management-development/g6/s2-result.json` |
| G6 原生集成 / schema / GoWAA 冷启动 | 7/7，0 失败/错误/跳过 | 同上；不含真实 WAA 联网 |
| M1 仓储/迁移模块 | 46/46 | 23 仓储、11 合成 SQLite SQL、6 新迁移、6 旧迁移；非 MySQL |
| M2 新后端模块 | 40/40 | capability/adoption/source event 与已有新后端检查 |
| Manager 原 Aitoapi 回归 | 187/187，0 失败/错误/跳过，20.657s | M4 `m4-corrective-final-20261020/legacy/result.json` |
| M4 实际 Python 模块组合 | 15/15，0 失败/错误/跳过，0.687s | M4 `m4-corrective-contract-20261020/contract/result.json` |
| M3 来源并发与新旧 UI | 57/57，0 失败/错误/跳过，1.792s | 含同 source epoch revision 乱序新增复现回归 |
| M4 最终新旧 Qt 离屏组合 | 69/69，0 失败/错误/跳过，3.671s | M4 `m4-corrective-r5-final2-20261020/joint-qt/result.json` |
| M4 实际 Go HTTP × 新 Manager 完整流程 | 21/21，0 失败/错误/跳过，56.234s | M4 `m4-corrective-contract-final-20261020/go-http/result.json`，隔离拒绝 0 |

M4 结果路径均以 `G:/code/gemini30/gemini-manager/runtime/devices/aistudio2api-acceptance/` 为根。各组有交叉，不相加为不重叠总数。总控独立核对 G6、M4 最终 HTTP/Qt 的结果 JSON、取消未知只读断言、Go 输入清单及二进制 SHA256；最终 fixture 为 `7AA640F7DFE006C134B20033895D026464158B84D3B9D08C2A1CD600AAB022E0`，未因测试预期校正重新构建或修改业务源码。

六项缺陷在上述离线/合成范围内关闭。真实 MySQL（用户延期）、macOS/浏览器实机、Linux 服务运行、真实 Google/Playground/两种 WAA 联网及生产启用仍未执行；没有提交、推送、部署或生产迁移，没有调用真实模型。
