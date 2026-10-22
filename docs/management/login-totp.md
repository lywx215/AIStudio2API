# Google Authenticator 2FA 同步与自动登录

Manager 个人、企业账号的 `twofa_secret` 是密钥来源。上传确认 Cookie 提交成功后，独立同步密码和 2FA 密钥；“同步登录凭据”也可单独更新这两项。缺少某项只跳过该项，保留服务器原值。清除远端 2FA 密钥只删除服务器保存的副本，不关闭 Google 两步验证。

服务器按用户要求在账户目录的 `login-totp.json` 明文保存原始 `email`、`twofa_secret`，并保存账户 UUID、独立版本和最近操作 ID。文件使用私有权限和原子替换，与密码共用账户文件租约；占用时返回 `ACCOUNT_BUSY`，不打断请求。Cookie 替换及归档保留此文件，账户删除时一并删除；删除恢复日志只保存完整性证据，不复制密钥。

## 管理接口

能力字段为 `loginTotpSync`。`/api/manage/v1/accounts/{id}/login-totp` 的 GET 使用 `accounts:read`，仅返回 `configured`、`version`、`lastOperationId`；PUT 使用 `accounts:write`，接收 `email`、`twofa_secret`、`expectedVersion`、UUID v4 `operationId`；DELETE 使用相同字段但不带密钥。清除保留版本墓碑，旧版本写入被拒绝。密码接口及其版本保持独立。

Manager 使用现有实例管理 Key、准确 HTTPS origin 和禁止授权重定向的通道。持久请求与历史仅保存安全操作身份和结果元数据；断线或持久化结果不明时，下次动作只读核对，绝不重放不明写入。密码与 2FA 分项报告，任何同步失败均不撤销已确认的 Cookie 上传。

## 登录行为

已有重新登录流程读取密钥并在服务器生成六位、30 秒、SHA-1 TOTP。计算时沿用 Manager 的去空格、去连字符、转大写和 Base32 补位规则，存储原值保持不变。剩余不足 8 秒时等待下一周期，等待期间继续检查取消和手动接管。单次登录最多自动提交一次验证码。

Google 的 Authenticator 页面可能分步加载邮箱、输入框与按钮。已识别为该页面时，最多等待 10 秒让控件就绪，并显示“等待 2FA 验证页面就绪”；这段加载过程不会永久关闭自动填写。提交选择唯一可用的真实按钮，避免在容器已出现、按钮仍不可用时提前点击。超时、控件歧义或用户接管仍转为手动处理。

如果 Google 先显示验证方式选择页，仅在精确 Google 登录来源、邮箱匹配、已保存有效密钥且 Authenticator 入口唯一可用时，自动选择明确的“Get a verification code from the Google Authenticator app”及支持的中文入口，每次登录只选择一次。入口缺失或歧义时等待或转为手动处理，不猜测其他验证方式。

填写仅限精确 `https://accounts.google.com`、明确的 Authenticator 页面、匹配的邮箱以及唯一可用输入框和提交入口。密钥不发送给浏览器，验证码仅用于本次 Google 提交。缺少或无效密钥、错误验证码、已有人为输入、页面或控件不明确时转为手动处理。短信、手机确认和 CAPTCHA 不自动处理。

保留 Do this later 和现有 AI Studio 就绪、邮箱核对、Drive 授权、认证保存、账户恢复和模型刷新流程。完成 2FA 本身不代表登录完成，取消或保存前失败不覆盖旧认证。

## 本轮验收边界

本轮交付本地代码与离线测试。用户选择暂不部署 ais130，因此没有同步真实 2FA 密钥或执行真实 Google 2FA 登录。发布时先升级服务端，再使用新版 Manager 同步登录凭据；只选择实际需要重新登录且已有密钥的账号验收，不主动使就绪账号失效。Windows 合成测试不替代真实 MySQL、macOS 或 Google 页面验收。
