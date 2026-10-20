# 验证通道选择与中文界面交付记录

日期：2026-10-20。状态：本批本地实现与合成上游联测完成；按用户后续要求，发布目标为 `dev` 分支，生产部署未执行。

本需求替代此前“验证固定 Playground”的限制。历史报告按当时范围保留，不用于证明本次新增能力。

## 已实现的行为

- 新建实例默认 Build；可单独选择 Playground，也可同时选择两个通道。
- 现有实例保留已保存的选择，可在实例编辑窗口修改；保存设置不调用模型。
- 两个通道各自固定同一账号、模型及凭据/状态版本，全部成功才判定整体验证通过。
- 失败、超时、取消或中断时保留已完成通道结果；不借用其他通道或账号，不重放结果未知的模型请求。
- 仅上传凭据（`verify:false`）不发起模型生成。
- 账号操作、绑定状态、批次进度、结果原因及保存/取消按钮使用中文。原始机器码和具体诊断保留在详情或提示中，协议及存储值不变。

例如截图中的 `capture · succeeded · CREDENTIAL_SAVED` 现在显示为“采集凭据 · 执行成功 · 凭据已保存”。Build、Playground 保留通道名称。

## 协议与兼容性

管理请求新增 `channels`，新请求省略时为 `["build"]`。只接受单通道或规范顺序的 `["build","playground"]`；反向输入在幂等摘要前规范化，空值、重复项和未知通道被拒绝。

任务结果新增 `channelResults`，分别保存实际通道、请求标识、账号索引、模型、双版本、上游状态和错误。单通道保留原顶层结果字段；双通道不伪造单一 `actualChannel`，也不把 `both` 当作实际上游通道。

已经受理、但私有任务数据不含通道字段的旧任务保留 Playground 语义，不改写原幂等摘要。服务重启后运行中任务标记中断，不重放上游动作。

Manager 使用 `validation_channels` 列表。既有配置列保存 `build/playground/both` 模式，逐通道证据存入既有 JSON；本次无需新增数据库迁移。配置变化会撤下不满足新通道要求的可用状态，旧 Playground 证据不能满足 Build 或双通道验证。

遇到只支持 Playground 的旧服务时，选择 Build 或双通道会明确提示远端需要升级，不静默降级。旧 Aitoapi 类、签名、默认行为与独立绑定继续保留。详细协议见 [verification-channels.md](verification-channels.md)。

## 分工

| 负责人 | 交付范围 |
| --- | --- |
| G0 | Go 公共协议、路由/任务、独立验证资源、恢复与 Go 测试夹具 |
| M2 | Manager 客户端、仓储、业务执行、读回和证据判定 |
| M3 | Manager 中文界面、通道选择和对应界面测试 |
| G6 | 独立 Go HTTP × 实际 Python 客户端验收 |
| M4 | 实际 Manager 组合、Go HTTP × Qt 联合验收 |
| 总控 | 跨模块核对、冻结测试源、补充回归及本记录 |

## Go 最终冻结与构建

本批修复了 `DEF-G6-CHANNEL-001`：Build 已完成、Playground 尚等待时，任务曾只持久化子结果，没有同步保存 `TaskItem.index`。实际 Manager 因无法完整确认归属，将已完成的 Build 误标为失败；重启后同样受影响。

修复后，检查点原子保存可信账号索引和子结果，拒绝账号身份或已有索引不一致的写入。Manager 严格归属校验保持不变，测试不注入缺失索引。G0 增加运行中、取消保留、不一致拒绝及 SQLite 恢复测试。

另一个独立用户任务“优化用量页加载速度”同时在正式目录修改 API、请求统计和前端。其临时 Go 文件曾使正式目录全量 vet 失败；这些改动全部保留，未覆盖或回退。为取得本批可重复证据，总控建立测试用源快照，并逐项核对 **331 个冻结 Go/SQL/模块/OpenAPI 输入**。快照不是 Git 分支或 worktree。

快照位置：`C:/Users/lywx2/AppData/Local/Temp/aistudio2api-channel-v3-source-eac5d9da27`。

| 实际命令 | 结果 |
| --- | --- |
| `go test -p 2 ./... -count=1 -timeout=2m` | exit 0 |
| `go vet -p 2 ./...` | exit 0 |
| `go test -c -o <GOTMPDIR>/g6-app.test.exe ./internal/app` | exit 0 |
| Windows CLI 编译 | exit 0 |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` CLI 交叉编译 | exit 0 |
| 331 个冻结输入执行前后 SHA256 比对 | 0 项变化 |

Windows 为实际执行环境；Linux 交叉编译不代表 Linux 实机运行验收。macOS 实机未验收。

最终 v3 夹具：`C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe`。

SHA256：`6752A62C86B42474CC609BB90F9928817054CFB620F377A8D61E29F088B5D7F6`。

命令日志、源清单及结果在同一 GOTMPDIR 的 `channel-selection-snapshot-v3-*` 文件中。`channel-selection-snapshot-v3.json` 记录因其他任务而漂移的三个旧文件、排除的四个新增 Go 文件及 331 项哈希一致性；它不证明其他任务的最终整合结果。

## 跨语言与界面验证

最终 v3 的 G6 严格复验已通过：V08/V11 定向 **2/2，9.064 秒**；完整通道矩阵 **12/12，2.639 秒**；旧 HTTP 回归 **22/22，8.143 秒**。全部 exit 0，无失败、错误或跳过。17 个实际 Manager 源码、Python driver 和 exe 输入前后一致，未修改或弱化 Python 断言。`DEF-G6-CHANNEL-001` 已关闭。

M4 最终 v3 实际组合：**Manager HTTP 29/29，83.390 秒；中文 Go→Qt 2/2，8.531 秒**。两组均无失败、错误、跳过或隔离拒绝，161 个相关源码输入、Python driver 和 exe 前后一致。沿用界面源码未变的 84 项离屏结果，不重复累加。

总控已直接核读两组原始 `result.json`，并独立比对 M4 前后 161 项源码、driver 与 exe。M4 最终限定静态检查也通过：12 个所属文件中 7 个 Python 语法、1 个 JSON 语法、敏感值模式及尾随空白检查均无问题。Manager 下最终机器汇总为 `runtime/devices/aistudio2api-acceptance/m4-channels-final-summary-20261020.json`，静态报告为 `m4-channels-final-static-20261020/result.json`。

G6 机器汇总：`runtime/management-development/g6/channel-v3-summary.json`；详细报告见 [channel-acceptance.md](channel-acceptance.md)。总控在交接时另比对正式目录 331 个冻结输入，仅上述其他任务的三个 API/requestdb 文件漂移，其余 328 项一致。

分模块文件清单、实际命令、接口变化及历史失败分别保存在 `docs/management/deliveries/G0-channels.md`、`docs/management/deliveries/G6.md`，以及 Manager 的 `docs/AISTUDIO2API_DELIVERY_M2.md`、`docs/AISTUDIO2API_DELIVERY_M3.md`、`docs/AISTUDIO2API_CHANNELS_ACCEPTANCE.md`。

已取得的独立证据如下，不把不同轮次或重叠用例累加为通过总数：

| 范围 | 实际结果及边界 |
| --- | --- |
| Manager 通道/业务/仓储相关 | M2 93 项通过；另 6 项合成迁移检查通过 |
| 原 Aitoapi 回归 | M2 本轮 17 模块、187 项通过，63.725 秒；是后续外来采集改动前的源码时期 |
| 中文界面 | 72 项相关检查通过；最终原因码补充后另 5 项中文流程检查通过 |
| 新旧 Qt 离屏组合 | M4 当前界面源码 84 项通过，3.500 秒；不代表 macOS 实机 |
| 当前仓储组合 | M4 15 项通过，0.610 秒；SQLite 有限语法适配不证明 MySQL 语义 |
| 后续外来采集改动的受影响路径 | 总控 6 模块、40 项通过，18.587 秒；两源 SHA 前后不变，只有 Node 弃用提示 |

总控已查看合成中文截图：`G:/code/gemini30/gemini-manager/runtime/devices/m3-aistudio2api-ui/capture-chinese-results.png`。标准独立启动器返回新进程 PID 48596，已有窗口保留。启动回执只证明进程启动，不充当完整 GUI 就绪或模型验证证据。

历史 v2（`FDFCB90A…FD74EE`）的 M4 29 HTTP、2 中文 Go→Qt、84 离屏均通过；G6 旧 22 HTTP 通过、新 12 项中 10 通过，剩余两项揭示上述检查点缺陷。因此 v2 不作为本批最终验收。更早 `BAF49E36…B514` 的控制连接收尾失败，以及测试输入/文案断言错误均保留在原报告；G0 修复夹具请求体消费与明确响应长度后，v2 的控制取消场景通过。没有将历史失败改写为通过。

## 保留的恢复边界

导入请求含客户端引用，可在受理响应丢失后有界只读定位原任务。现有模型验证请求不含该引用；若整个受理响应丢失，Manager 不能仅凭同账号候选任务唯一确认归属。

该情况下保持“结果待确认/需要核对”和原检查点，仅 GET 观察，不重发模型请求、不授予可用状态。测试私下知道的任务 ID 不得写入检查点。本次验收验证的是安全保留未知结果，不宣称已完成唯一定位或自动恢复成功。

## 前序操作与隔离

用户此前明确授权执行 `20261020_01_enterprise_aistudio2api`。只读预检确认仅缺该版本，复用现有显式迁移入口执行成功；后续确认 7 张 InnoDB 表、版本记录存在且无缺失版本。这是迁移执行证据，完整 MySQL 事务、分页、并发 CAS 验收仍按用户要求另行安排。

用户自行创建管理 Key；加密读回确认实例 1 已保存匹配凭据，无需重写。实际管理连接预检返回 `connection_valid:true`、`ready:true`；系统、账号、任务及空写入拒绝检查通过。模型 Key 密文未改，模型调用为零，Codex 未创建或撤销管理 Key。

M4 检测到其他任务对 `enterprise/aitoapi_capture.py` 与 `scripts/validate_aistudio_auth_import.py` 的采集/窗口收尾诊断修改。总控只读检查并补验上述 40 项，保留其原始错误和次级收尾错误；来源未确认，不归为本批实现，也未回退。

上述实现与验收阶段未提交、推送或部署；未执行新的生产配置写入、真实模型调用、GitHub Actions、Claude 或 Antigravity 审核。所有开发联测使用临时账号、合成凭据和假上游。既有生产版本未据此更新。

## 后续本地组合检查与分支发布

用户随后要求检查各项本地变更，再明确要求在 AIStudio2API 新建 `dev`、提交并推送远程。本次分支包含当前通道功能、用量页优化及对应测试和文档；不包含 Manager 或 Aitoapi-custom 仓库的文件。

用量页任务完成后，总控在正式目录重新执行全量 Go 测试和 vet、32 项前端测试、类型检查及 lint，全部通过。由当前正式源码另行编译的独立测试夹具通过实际 Python 客户端的 12 项通道矩阵和 22 项原 HTTP 回归；夹具 SHA256 为 `F2734513D138A5EF3736A3B73E7AD3A75EBCD2AB6845BDC3F1D29BDE56ECF0FD`。它补充了此前冻结快照之外的当前组合验证，不改写各阶段历史记录。

该次复核的 399 个源码输入指纹前后一致，三个正式项目无未合并条目或源码冲突标记。发布只针对 AIStudio2API 的 `dev`，生产部署与真实模型验收仍另行安排。
