# 登录密码同步与重新登录

Manager 是密码来源。个人和企业账户的普通上传、覆盖、简要采集上传，在 Cookie 提交确认成功后独立推送密码；也可使用“同步登录凭据”分别同步密码与 [2FA 密钥](login-totp.md)，或显式“清除远端密码”。空密码只跳过，不清除远端原值。密码同步失败不撤销已完成的 Cookie 上传，界面分别显示各项结果。

远端按用户要求在账户目录的 `login-credentials.json` 明文保存 `email` 和 `password`，密码空格原样保留。该文件不属于 storage state，不进入 Cookie 导出；版本、账户 ID 和最近操作 ID 用于身份检查和并发控制。无需加密密钥配置。Cookie 更新和归档保留此文件，删除账户一并删除。

## 管理接口

`GET /api/manage/v1/accounts/{id}/login-password` 使用 `accounts:read`，仅返回 `configured`、`version`、`lastOperationId`；首次版本为 0，操作 ID 为空。

同一路径的 `PUT` 使用 `accounts:write`，body 为 `email`、`password`、`expectedVersion`、`operationId`（UUID v4）。`DELETE` 使用相同权限与字段但不带 `password`。返回统一管理 envelope，始终不回显密码。密文、普通密码哈希和密码本身均不进入任务历史；密码操作是同步接口，不创建模型任务。版本冲突拒绝旧写入，同一操作 ID 的相同请求可幂等读回；清除后保留版本墓碑。

能力接口的 `loginPasswordSync` 表示是否支持。Manager 使用已有管理 Key、固定 HTTPS origin 和禁止授权重定向的传输；写入前持久保存无密码的操作身份，断线后的后续动作只按原操作查询状态，不重放未知写入。查询仍不明时保持“需核对”，不能把普通版本变化当成本次写入成功。

## 重新登录

点击“重新登录”会在现有隔离 Camoufox 中自动填写邮箱和已同步密码，复用原代理、指纹、语言和时区。已同步 2FA 密钥的账号可自动填写 Authenticator 动态码。填写仅限 HTTPS `accounts.google.com`；各阶段最多自动提交一次。缺少凭据、验证错误、人机验证码、手机确认或未知页面时使用现有窗口手动处理；“手动接管”停止后续自动输入与点击。

Google 的可延期两步验证注册提醒会识别唯一可见的 `Do this later`、`以后再说`、`稍后再做` 或 `稍后执行此操作` 入口，仅在明确提醒页执行一次。跳过提醒不是登录成功；继续等待 AI Studio 可用并核对原邮箱，随后沿用既有 Drive 检查、认证保存、账户恢复和模型刷新。保存前取消或失败保留旧认证。

升级顺序：先服务端，再 Manager，然后同步密码并验收重新登录。离线夹具覆盖的行为不等同于 Google 实际页面验收。
