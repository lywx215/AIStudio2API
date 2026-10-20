# 上游 v0.2.14 合并记录

日期：2026-10-20。以本地 `dev` 为基准完整合并上游，保留自定义管理协议、定向验证、用量页与错误呈现行为。所有功能验收只使用合成账号、临时数据库、假上游和本机 loopback fixture。

## 基准与保护

- 本地与 fork `dev` 基准：`bdfe9eb867024f6b099c677773fe0c71940bb57f`。
- 上游：`Mag1cFall/AIStudio2API`，`370a313814099b508881807effa2a9d72f2d7e66`，发行版本 `v0.2.14`。
- 共同祖先：`00ad27b5947b0feb2f7fc4dde0c0853ac2a54200`；两侧各有 5 个独有提交。
- 三方比较：本地 163 个改动文件、上游 283 个、共同改动 37 个、文本冲突 11 个。本地独有的 126 个文件全部保留。
- 本地备份引用：`backup/dev-before-upstream-20261020`。
- 原工作目录的 `auth/` 和 `runtime/requests.db` 已独立备份至忽略目录 `runtime/backups/dev-before-upstream-20261020-bdfe9eb/`；数据库使用 SQLite 在线备份并通过 quick_check。原目录没有 `.env` 或 `data/`。
- 更新在独立工作区完成，不启动正常生成服务、不读取部署 Key、不执行真实模型调用或生产操作。

## 最终合并规则

1. 保留管理 API 的独立认证、冻结 schema、任务持久化、取消/重启恢复、原始路径分派、账号协调写入与 epoch/CAS。新服务启动结构同时注册管理协议、promptdb、会话持久文件与媒体处理。
2. 新 LiveView/CookieBrowsers 委托采用本地短锁 generation 快照；Cookie/浏览器导入仍走协调持久化，保留原子写入、父目录同步及指纹状态。生产模型通道设置与独立管理定向验证保持分离。
3. URL Context 统一为本地独立事件与逐 URL 累积，修正重复类型/解码，grounding 不能覆盖已取得的 URL 读取状态。
4. 保留固定对外错误文案与内部诊断、Retry/上下文识别信号；只有 `401 + admin_login_required` 使控制台退出。新 Playground 错误解析、异常 EOF、共享状态和迟到 SSE/请求同样受到约束。
5. 接收上游 Playground、Router、桌面支持、Cookie 导入、模型/协议扩展和媒体链接；保留管理密钥入口、密码会话门控，以及用量四页按需查询、缓存、取消和图表懒加载。
6. 新媒体配置保持旧配置默认 `inline`，JSON/env/原生 DTO 保存不丢失新增字段。
7. CLI 保持默认服务构建入口；提供 `scripts/build-custom.ps1` 分别构建 CLI/桌面。fork CI 覆盖 dev 和本地 Node 测试，CLI/桌面/Docker 均注入 `managedBy=lywx215`；Docker 发布依赖回归构建。定制更新模式阻止官方二进制覆盖本地功能。

## 实际验收

| 检查 | 结果 |
| --- | --- |
| 前端 Node 回归 | 46/46 通过 |
| 前端 typecheck / lint / format:check / 正式 build | 通过；仅有上游大型 chunk 提示 |
| Go API / aistudio 定向 URL、媒体、错误回归 | 通过 |
| 管理生命周期、配置兼容、managedBy 定向回归 | 通过 |
| 全量 Go test | exit 1：其余包通过，`managementstore.test.exe` 被 Windows Application Control 阻止启动，未执行该包断言 |
| 最终 HTTP 修复影响回归 | management、app、api 全包通过；未重复运行系统受阻包 |
| CLI 与 desktop,production Go vet | 最终源码均通过 |
| Windows CLI / Windows 桌面 / Linux amd64 交叉构建 | 均通过；含 `v0.2.14-custom.1` 与 `lywx215` 标记，不启动产物 |
| Chrome 用量页回归 | 9 项通过，使用原行为断言 |
| Chrome 导航与会话回归 | 5 项通过，零浏览器异常、零模型请求 |
| 实际 Manager 客户端通道联测 | 最终新 fixture 12/12 通过，0失败/错误/跳过；源文件与 fixture 前后哈希稳定 |
| 实际 Manager 旧 HTTP 联测 | 最终新 fixture 22/22 通过，0失败/错误/跳过；原断言保留 |

Windows 阻止原文：`An Application Control policy has blocked this file.` 没有更名、迁移或重新执行受阻测试程序来规避策略，也不将这项记为通过。独立 app HTTP fixture 不替代被阻止的 managementstore 单元测试。

浏览器和 HTTP 证据保存在本次工作区忽略目录 `runtime/usage-verification/`、`runtime/acceptance/`。macOS/Linux 桌面实机、Docker 镜像构建、真实 Google/Camoufox 和生产验收未执行。


## HTTP 连接收尾修复及复验

首轮旧 HTTP 完整组为 21 通过、1 个 Windows `ConnectionResetError`（10054）；保持原 fixture 和严格断言的修复前单项复现通过，表明是连接收尾竞态。Go 服务在 `Connection: close` 下提前拒绝跨站 POST 时不预先排空请求体，可能使正确的 403 JSON 响应遭遇 TCP reset。

共享管理响应边界现对提前拒绝的残体最多排空 64 KiB（额外 1 字节探测）并限定 250 ms，超限/超时关闭连接，Expect:100-continue 拒绝不触发上传；显式 Content-Length 与刷新使响应完整。原 403、认证、权限与账号变更规则均保留。新增真实 TCP 回归覆盖小体关闭/复用、超限、慢体及 Expect 场景。

修复后的 fixture SHA256：`5692047F61005BEE2DF23ADD5890B3928184E701319F07492F492E670436C072`。旧 HTTP 22 项和通道 12 项均以该文件完整通过，执行前后哈希相同。最终证据为 `runtime/acceptance/management-http-final.log`、`channel-http-final.log`、`channel-result-final.json` 和 `compile-final.log`；首轮错误及修复前单项结果没有覆盖或抹除。此次没有修改 Python driver 或弱化断言。

## 推送前复核（正式项目 dev）

用户授权检查、合并并推送本地变更后，在正式目录重新核对。复核起点为已存在的合并提交 `3abd4a46fb359dc4ba9db3d47683c789d2fc9584`；正式目录和同步工作区均无未提交文件，远程 `dev` 仍为 `bdfe9eb867024f6b099c677773fe0c71940bb57f`，本地领先 6 个提交。合并已经包含旧 `dev` 和上述上游历史，无须重复合并或挑选提交。

- 索引无未解决项，源码无冲突标记，`git diff --check origin/dev..HEAD` 通过。旧 `dev` 相对共同祖先新增的 121 个路径全部保留；该计数与前述“本地独有文件”统计口径不同。
- 对本次待推送的 48,229 行新增文本执行密钥模式扫描，未发现匹配；没有收录账号、运行数据库、凭据、本地二进制或构建产物。
- 正式目录原有 `node_modules` 缺少上游新增依赖，首次类型检查失败。执行 `npm ci` 按现有锁文件同步后，未改写依赖清单或锁文件；最终 Node 回归 46/46、typecheck、lint、format:check 均通过。前端构建通过，输出到独立临时目录；保留既有大 chunk 提示。
- `go test -mod=readonly -p 2` 对 api、app、aistudio、management、managementcontract、managementmaintenance、managementverify、config、chromeauth、camoufoxnative、setup 执行回归（`-count=1 -timeout=2m`）：除 api 外的 10 个包通过；`api.test.exe` 本轮被 Windows Application Control 阻止启动，整体 exit 1，API 断言未执行。未重试该程序，也未重跑先前受阻的 managementstore/requestdb 测试程序，不将旧轮次通过记录替代本轮结果。
- `go vet ./...` 首次扫入被 Git 忽略的 `runtime/review-20261020/media_output_review_test.go`，因独立评审片段缺少 `NewHandler` 而失败。未删除或改写该临时文件；改对正式源码 `./cmd/... ./internal/...` 执行 `go vet -mod=readonly -p 2`，CLI 默认路径与 `-tags desktop,production` 路径均通过。
- Windows CLI 和 `desktop,production` 编译检查通过，使用既有内嵌资源并输出到独立临时目录，没有启动或交付这些检查产物。首次命令的 PowerShell `ldflags` 参数未展开，改用参数数组后完成编译。
- `npm audit --json` 报告 4 项依赖告警（2 高、2 低）：Vue/server-renderer 及 KaTeX/markdown-it-katex 依赖链。仅记录检查结果，本次没有执行自动修复或依赖升级。

本次提交保留既有合并历史，并补充此复核记录；推送的最终提交附带 `[skip ci]`，本轮不执行 GitHub Actions。未部署、重启、迁移数据库或调用真实模型；Windows 以外的平台未重新实机验证。
