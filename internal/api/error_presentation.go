package api

import "net/http"

// clientErrorMessage uses a closed vocabulary. Never copy provider errors,
// account names, model IDs, routes or request values into user-facing errors.
// Protocol status/codes remain available to clients; original diagnostics stay
// in the existing access-log metadata before presentation is applied.
func clientErrorMessage(status int, code string) string {
	switch code {
	case "admin_login_required":
		return "请先登录管理控制台。"
	case "invalid_credentials":
		return "用户名或密码错误。"
	case "invalid_api_key":
		return "Invalid API Key"
	case "request_canceled":
		return "请求已取消。"
	case "context_length_exceeded":
		return "输入内容过长，请缩短后重试。"
	case "account_busy":
		return "账号正在处理其他请求，请稍后重试。"
	case "account_required", "account_unavailable":
		return "服务暂时不可用，请稍后重试或联系管理员。"
	case "invalid_config", "invalid_account":
		return "设置无效，请检查配置后重试。"
	}
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return "请求参数无效，请检查请求格式与必填字段。"
	case http.StatusUnauthorized:
		return "认证失败，请检查认证信息或联系管理员。"
	case http.StatusForbidden:
		return "当前请求无权限，请联系管理员。"
	case http.StatusNotFound:
		return "请求的资源不存在或不可用。"
	case http.StatusConflict:
		return "资源状态已变化，请刷新后重试。"
	case http.StatusRequestEntityTooLarge:
		return "请求内容过大，请缩小后重试。"
	case http.StatusUnsupportedMediaType:
		return "请求格式不受支持，请检查 Content-Type。"
	case http.StatusTooManyRequests:
		return "请求过于频繁或暂时达到使用上限，请稍后重试。"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "请求处理超时，请稍后重试。"
	case http.StatusNotImplemented:
		return "当前功能暂不支持。"
	case http.StatusServiceUnavailable, 529:
		return "服务暂时不可用，请稍后重试。"
	default:
		return "请求处理失败，请稍后重试或联系管理员。"
	}
}

func clientRequestErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return clientErrorMessage(statusFromError(err), openAIErrorCode(err))
}
