# G6 selectable-channel acceptance

2026-10-20。当前 **canonical v3严格复验PASS：V08/V11定向2项→完整通道12项→旧HTTP22项依次全部通过**。DEF-G6-CHANNEL-001在v3关闭，最初失败/v2结果保留历史。G6未改Go/fixture/Manager或两个Python driver源码，也未重新编译；未弱化实际索引校验。G0负责Go和fixture，M2负责实际新客户端/证据，M3负责UI。

## Canonical fixture v3 最终严格复验（当前结论）

新exe：`C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe`，执行前后SHA256均 **6752A62C86B42474CC609BB90F9928817054CFB620F377A8D61E29F088B5D7F6**。G0已把可信`input.Account.Index`与验证Result放进同一次Checkpoint保存，并拒绝账号/索引不一致；G6原Python断言与实际M2严格索引校验均保持。

为隔离另一用户用量页的并发修改，本次canonical Go构建来自总控显式测试源快照 `C:\Users\lywx2\AppData\Local\Temp\aistudio2api-channel-v3-source-eac5d9da27`，不是分支/worktree。G6只读复核总控原始 `channel-selection-snapshot-v3.json` 与 `channel-selection-snapshot-v3-result.json`：331本批冻结输入哈希全部匹配，tests/vet/fixture/Windows/Linux builds均exit0，前后SourceChanged=0。快照不含Python runner；G6以该快照为工作目录，使用正式项目的**绝对路径Python runner**与实际Manager源码，不复制或改动它们。Linux证据仍仅crossbuild。

| 顺序 | 实际命令范围 | exit | 结果 |
| --- | --- | --- | --- |
| 1 | V08取消与V11重启严格定向 | 0 | **2项/0失败/0错误/0跳过，9.064s** |
| 2 | V01–V12完整通道矩阵 | 0 | **12项/0失败/0错误/0跳过，2.639s** |
| 3 | 旧22项真实HTTP完整回归 | 0 | **22项/0失败/0错误/0跳过，8.143s** |

定向2项是完整12项的子集，不将三次命令累加成36个独立场景。V08验证Build完成、PG仍等待时实际Manager可确认Build成功，随后真实cancel保留该子证据；V11验证kill/restart后同一raw Build子结果和Manager成功子状态保留，同时aggregate保持未完成/不可授双成功，且重启无模型重放。**DEF-G6-CHANNEL-001已关闭**，不是用测试补index或放宽校验获得通过。

V10在本轮完整组PASS：测试ack完全丢失、无taskId/无clientRef时，实际Managerreadback只GET并保留unknown/candidate，不认领同账号候选、不授available/verified、不重POST/模型。此为安全未知结果处理，**不承诺模型task唯一定位成功**；旧import ack有clientRef的真实定位是另一场景。

本轮严格前后hash一致：**17输入**（14相关Manager源码+2个Python driver+canonical exe），SourceDriverExeStable=true。两个driver与v2失败时hash相同：`management_acceptance.py=BBE9FED442F277954DA1A7A52D639291AD515EEE23039C1E62F1E2A2A4D2EDEA`；`management_channel_acceptance.py=9D5E906A9E35F4594CCE0481685812907EF1CDDF2A9A73404EFD64FEB268DF93`。这支持“未弱化失败断言”的证据；没有重新编译或改源码。

实际顺序命令（均在上述快照工作目录，正常同用户执行上下文）：

```powershell
G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B G:/code/gemini30/AIStudio2API/scripts/management_channel_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe --fixture-sha256 6752A62C86B42474CC609BB90F9928817054CFB620F377A8D61E29F088B5D7F6 --test test_v08_cancel_preserves_completed_build_without_dual_success --test test_v11_crash_retains_completed_build_and_does_not_replay_playground --output G:/code/gemini30/AIStudio2API/runtime/management-development/g6/channel-result-v3-targeted.json
G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B G:/code/gemini30/AIStudio2API/scripts/management_channel_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe --fixture-sha256 6752A62C86B42474CC609BB90F9928817054CFB620F377A8D61E29F088B5D7F6 --output G:/code/gemini30/AIStudio2API/runtime/management-development/g6/channel-result-v3-final.json
G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B G:/code/gemini30/AIStudio2API/scripts/management_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe
```

安全产物 `runtime/management-development/g6/`：`channel-http-v3-targeted.log` / `channel-result-v3-targeted.json`、`channel-http-v3-final.log` / `channel-result-v3-final.json`、`python-http-selectable-v3-final.log`、`channel-v3-summary.json`、`channel-v3-source-driver-exe-before.json` / `...-after.json`。不含ready或Key。旧BAF49...与FDFC...失败日志/结果不覆盖，以下保留历史。

G6本轮无剩余业务失败或自身验收项。真实模型/Google、真实配置凭据、生产、MySQL（用户延期未准备）、macOS/实际Camoufox及Linux运行均未执行；完整Manager service/repo/UI与最终M4复核另由总控调度。不提交、推送、部署，不创建额外会话/代理。

## Canonical fixture v2 整组执行（历史失败，已被v3取代）

总控交接新exe SHA256 **FDFCB90A42B51C5993EB630FB90EFCE7BFD13384C66E92A06DB9B07848FD74EE**，路径仍为 `C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe`。相对前一BAF49...版本只更新测试fixture的release连接收尾，生产Go/SQL/OpenAPI由总控确认相同。G6没有编译、修改Go或Manager/driver源码。

| 真实命令组 | exit | 本轮实际结果 |
| --- | --- | --- |
| 旧22项HTTP完整回归（显式Playground） | 0 | **22通过，0失败/错误/跳过，8.701s**；包括before-prepare取消/release，没有连接收尾异常 |
| 当前V01–V12完整通道矩阵 | 1 | **12项：10通过、2失败、0错误/跳过，8.272s**。V08与V11仍是DEF-G6-CHANNEL-001；整体不通过 |
| source-only OpenAPI/错误对 | 0 | 350内部refs、21 legacy错误对，httpExecuted:false |
| 相关Manager+driver+exe前后hash | 0 | **17输入完全相同**：14相关Manager源码+2 Python driver+1 exe；SourceDriverExeStable=true |

V10已按冻结合同修正并在此完整组实际通过：模型ack丢失后没有taskId，不认领同账号候选，实际Managerreadback仅GET保留unknown/candidate、不授成功/available、不重POST/模型；这是未知结果安全性，不是唯一定位成功。

V08：真实raw Build子结果已完成，PG挂起时TaskItem.index仍null，Manager子结果failed；subTest保留失败后继续真实cancel，最终cancel及raw Build保留/Manager已完成子结果都通过。V11：实际kill/restart后raw Build子结果保持，但item.index仍缺，Managerchild状态failed。这不是release传输错误；两失败都为业务断言，**需总控协调G0/M2修复归属完整性**。G6没有补index或放宽校验，没有重复失败套件寻求偶然通过。

新旧哈希日志/结果分开保留。本轮安全产物在 `runtime/management-development/g6/`：`python-http-selectable-v2-final.log`、`channel-http-v2-final.log`、`channel-result-v2-final.json`、`channel-v2-summary.json`、`channel-v2-source-driver-exe-before.json` / `channel-v2-source-driver-exe-after.json`。两个driver执行前后hash分别为：

- 旧runner `BBE9FED442F277954DA1A7A52D639291AD515EEE23039C1E62F1E2A2A4D2EDEA`。
- 通道runner `9D5E906A9E35F4594CCE0481685812907EF1CDDF2A9A73404EFD64FEB268DF93`。

本轮命令（同正常用户执行上下文，只合成loopback数据/假上游）：

```powershell
G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B scripts/management_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe
G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B scripts/management_channel_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe --fixture-sha256 FDFCB90A42B51C5993EB630FB90EFCE7BFD13384C66E92A06DB9B07848FD74EE --output runtime/management-development/g6/channel-result-v2-final.json
```

MySQL未准备，真实配置/Key/Google/模型、生产、其他平台均未访问或执行。以下BAF49...结果为历史，不冒充当前v2。

## 首轮BAF49...实际结果与缺陷发现（历史）

总控canonical fixture SHA256 **BAF49E36D8977B63C88576037E97195E329B8F631FF9C02F146425DEC677B514**，路径为 `C:\Users\lywx2\AppData\Local\Temp\aistudio2api-management-go-tmp\g6-app.test.exe`；执行前后一致。新独立runner `scripts/management_channel_acceptance.py` 直接导入实际Manager client/readback/verification/evidence；14个相关Manager源码SHA256在旧回归+矩阵前后完全一致。没有反射能力shim、fake HTTP响应或业务修改。

- 显式Playground适配后的原22项：exit0，22项/0失败/0错误/0跳过，11.987s，日志 `runtime/management-development/g6/python-http-selectable-regression.log`。
- V01–V12首轮：exit1，12项，11项通过/1失败/0错误/0跳过，3.380s，`sourceStable:true`。V08在PG尚等待时Manager将已成功Build子证据误标failed；日志 `channel-http-v1.log`、完整源hash与计数 `channel-result.json`。
- 保留失败断言，用subTest继续实际cancel，并给V11加上同等“重启后Manager仍应识别Build完成”断言后，仅定向V08/V11：exit1，2项/2失败/0错误/0跳过，0.972s，`sourceStable:true`。V08真实cancel后raw Build子证据与Manager结果均保留成功；V11raw Build保留但Manager子结果错误。日志 `channel-http-diagnostic.log`、`channel-result-diagnostic.json`。
- 上述都是不同命令，不相加当成通过数；新增完整当前矩阵尚未重跑/不宣称通过。静态 `--list-tests`列12项；旧runner `--check`exit0，350 OpenAPI refs/21错误对/httpExecuted:false。

### V10 证据边界修正（原先“唯一定位成功”结论撤销）

总控/M4确认冻结TestRequest没有clientRef，实际readback仅能唯一定位import submission。原V10自行按同账号/key/time候选task扫描，再将该task交给Manager成功证明，证据超过合同；**该V10通过不作为恢复成功证据，已撤销并删除该逻辑**。不把fixture已知taskId注入检查点，也不新增定位协议。

新方法 `test_v10_lost_model_ack_stays_unlocated_with_read_only_readback`：实际POST ack丢失→不含remote_task_id的原始检查点→实际新Managerreadback，仅GET，finding必须account_candidate_only或remote_result_unknown；没有可认领remote_task_id/remote_task、verification/last_verified或available=true。仅一次POST；fake-target计数只证明未重放，绝不充当任务归属/Manager成功证据。

使用同一BAF49E36...exe定向运行：**exit0，1项，0失败/错误/跳过，0.417s，sourceStable=true**。日志 `runtime/management-development/g6/channel-http-v10-boundary.log`，hash/计数 `channel-result-v10-boundary.json`。这是“模型结果未唯一定位而安全保留unknown”的通过，**不是完成恢复/授予available**。实际Service/Repo needs_reconcile的更完整路径由M4验收。旧22项中的import ack丢失仍有clientRef与实际readback定位，为独立成功场景。V08/V11业务缺陷仍待修，未重跑无关整套。

### DEF-G6-CHANNEL-001：完成子结果缺TaskItem.index，Manager在运行中/重启后无法确认该子证据

真实复现：verify:false import→实际新client提交`channels=[build,playground]`/model=`g6-cancel-after-build`→真实Go完成Build并将PG挂起→GET任务。安全诊断文件 `runtime/management-development/g6/channel-v08-diagnostic.json`：

```json
{"taskStatus":"running","itemIndex":null,"accountIndex":1,"childAuthIndex":1,"childSuccess":true,"childStage":"model_verified","childRequestedChannel":"build","childActualChannel":"build","managerChildStatus":"failed"}
```

静态定位支持的原因：`internal/management/orchestration.go:206`的Checkpoint只写Items[index].Result；`internal/management/tasks.go:344`在最终execute返回后才写i.Index。M2实际`aistudio2api_verification.channel_verified`要求TaskItem.index为int且匹配account/index，再复用旧严格归属校验；`aistudio2api_evidence._child_snapshot`因此将已有成功Build子结果标failed。进程在PG等待时kill/restart后，raw Build子结果不变，但public item仍缺index，定向V11再次发现同一误分类。正常cancel走最终finish后有index，所以V08取消后的保留检查通过。

影响：不会错误授予双成功，但用户看到的已完成Build被误标失败，不满足“取消/重启保留已完成子结果”的实际Manager语义。建议总控交G0/M2协调安全归属字段在checkpoint/recovery时的完整性；G6不补index、不降低Manager校验，也不改Go/业务。修复后交接新exe/hash，仅定向V08/V11再完整矩阵验收。

实际命令：

```powershell
G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B scripts/management_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe
G:\code\gemini30\gemini-manager\.venv\Scripts\python.exe -B scripts/management_channel_acceptance.py --manager-root G:/code/gemini30/gemini-manager --fixture-exe C:/Users/lywx2/AppData/Local/Temp/aistudio2api-management-go-tmp/g6-app.test.exe --fixture-sha256 BAF49E36D8977B63C88576037E97195E329B8F631FF9C02F146425DEC677B514
# 定向诊断：再加 --test test_v08_cancel_preserves_completed_build_without_dual_success --test test_v11_crash_retains_completed_build_and_does_not_replay_playground --output runtime/management-development/g6/channel-result-diagnostic.json
```

以下为准备/需求历史，实际fixture已由G0按这些场景交付；上方缺陷结论优先。

## 已核对的准备边界

旧 `scripts/management_acceptance.py` 有隐含Playground请求：旧Aitoapi `submit_test`不发送channels，而结果断言要求actualChannel=playground；在默认Build后该组合不再成立。旧negative场景g6-buildonly也必须显式请求Playground才仍代表禁止换通道。G6已将旧通道专属探针改为 `submit_playground_test`，由实际旧Aitoapi transport显式发送`channels:["playground"]`，保留旧Aitoapi业务类/签名/默认行为。queued model/connection helper也显式选Playground。新readback使用稳定inspect API和checkpoint.validation_channels=[playground]，不让新默认Build重解释旧请求。仅导入verify:false不需要隐式验证通道。

新专项通过真实GoHTTP→实际 `AIStudio2APIManagementClient` / `AIStudio2APITaskReadback`，不注入假HTTP结果，不修改Manager。当前源码仍在并发改造：Go TestRequest暂未带channels，新Manager contract已定义默认build/canonical双选，但client/readback方法仍待M2交付。因此新矩阵只准备，不把接口缺口用测试shim伪装成功。

已收到的wire要求：省略channels默认[build]；显式[playground]或canonical[build,playground]；结果含channels与channelResults，每子requestedChannel/actualChannel/success/requestId/upstreamStatus/stage/authIndex/model/双版本/error；单通道仍顶层actualChannel。双通道不能拼造actualChannel=both。capabilities要求verificationChannels=[build,playground]、defaultVerificationChannels=[build]、multiChannelVerification=true。最终schema/方法签名按G0/M2交接对齐。

## 需要G0提供的fixture行为（明确需求，尚非已实现）

1. 保留既有g6-fixture-v1启停、显式临时目录、合成Key、/__g6__/state、auth-expired/claim-fault/提交屏障；ready仍只含安全端口和本次合成信息。新构建路径/hash由总控交接。G6不自行改Go fixture。
2. `g6-ok`对**请求的每个通道**返回同一指定账号/模型/版本、2xx/有效内容/终止且actualChannel等于requestedChannel；Build和Playground均需真实verifier/真实TaskService逐子处理，不用fixture直接编造aggregate/task成功。
3. state的buildCalls/playgroundCalls必须按fake-target**实际被要求执行的通道**累计，不能按故障observation伪称的actualChannel计数。generated=generationCalls为实际fake Run次数；opened/closed保持真实。最好有安全events序列 `{accountId,channel,model,mode}`，不含Cookie/正文/Key，便于证明没有换号/自动回退。控制计数不能由测试预置成功。
4. 部分失败场景建议model alias `g6-playground-fail`：Build真实fake-target执行成功，Playground执行返回失败；结果保留Build成功和Playground失败，aggregate与新Manager证据都不能算双成功。若G0采用其他alias，交接确切名字即可，G6仅适配测试常量。
5. `g6-cancel-after-build`：Build完成后，Playground fake Run阻塞到context取消；TaskService须在Playground等待期间已持久/可GET读出Build完成子证据。这样能实际cancel/kill/restart并证明保留已完成、未完成不补成功、不重放模型。若需要独立test-only barrier，使用/__g6__控制，不能加入生产namespace。不在DB事务里等待。
6. 保留g6-wrongacct/g6-wrongmodel；新增或指定`g6-wrongchannel`，对任一请求通道返回另一个actualChannel。失败不能换号/改通道重试。原g6-buildonly保留为显式Playground的反例。
7. 对错误channels（empty/duplicate/unknown/null等）实际handler给可信INVALID_REQUEST/不入队；canonicalization与reverse输入是否接受由G0最终wire明确。新Manager的reverse选择应canonicalize为build,playground；请求和结果顺序一致。

## 计划真实HTTP矩阵

| ID | 场景 | 真实断言 |
| --- | --- | --- |
| V01 | 真实capabilities/preflight | defaultVerificationChannels=[build]；multiChannelVerification=true；verificationChannels=[build,playground]；0模型 |
| V02 | wire省略channels | 只Build一次；结果channels=[build]，单通道actualChannel=build |
| V03 | 实际新Manager client默认提交 | 请求/结果默认Build，新readback接受且无Playground调用 |
| V04 | 新client显式Playground；旧Aitoapi transport显式Playground | 仅Playground，每次1调用；保留旧后端覆盖而不改其业务默认 |
| V05 | both成功（含Manager reverse输入canonical化） | 同task两个真实子证据均成功；各自requested=actual、requestId/2xx/account index/model/版本正确；build1+pg1；无actualChannel=both |
| V06 | Build成功/Playground失败 | 部分子证据保留；aggregate success/新readback不得model_verified双成功 |
| V07 | wrongacct/wrongmodel/wrongchannel | 明确失败、no fallback、no retry；按请求通道计数，另一通道0调用 |
| V08 | Build完成后取消Playground | GET到已完成Build→真实cancel→完成子证据不变；未完成不能成功；新Manager拒绝双成功 |
| V09 | verify:false含双选/上传/replace/readback | 真实提交与原receipt；generation/build/pg均0，不隐式验证 |
| V10 | 模型验证202响应丢失后无法唯一定位 | 实际transport收到后丢弃；无taskId检查点实际readback仅GET保留unknown/candidate，不认领同账号候选、不授available、不重POST或重放模型 |
| V11 | 双通道中途crash/restart | running interrupted，Build历史完成保留，重启计数0；不恢复未完成Google动作 |
| V12 | malformed selection | 可信400/INVALID_REQUEST，任务计数不增，generation0 |

M2依赖：实际client `submit_test(..., channels=...)`（或其冻结的等价参数）和新readback显式预期channels输入；默认省略为Build。旧checkpoint无channels按历史Playground，新checkpoint完整canonical选择。G6不通过反射或fake collaborator绕过缺失能力，不把旧Playground成功拼为Build/双成功。Qt/仓储/MySQL仍由M3/M4分层验收，MySQL用户已延期不准备。
