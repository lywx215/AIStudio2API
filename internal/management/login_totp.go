package management

import (
	"net/http"

	c "github.com/Mag1cFall/AIStudio2API/internal/managementcontract"
)

func (h *httpService) loginTotp(w http.ResponseWriter, r *http.Request, id string, p c.Principal, requestID string) (any, int, error) {
	service, ok := h.options.Accounts.(c.LoginTotpCoordinator)
	if !ok {
		return nil, 0, c.NewError(c.CodeInvalidState)
	}
	if r.Method == http.MethodGet {
		status, err := service.LoginTotp(r.Context(), id)
		return status, 200, err
	}
	raw, err := readObject(w, r, false)
	if err != nil {
		return nil, 0, err
	}
	var input c.LoginTotpInput
	if required(raw, "email", "expectedVersion", "operationId") != nil || decode(raw, &input) != nil {
		return nil, 0, invalid()
	}
	if r.Method == http.MethodDelete {
		fields, _ := object(raw)
		if _, exists := fields["twofa_secret"]; exists {
			return nil, 0, invalid()
		}
	}
	status, err := service.WriteLoginTotp(r.Context(), id, input, r.Method == http.MethodDelete)
	if err == nil {
		action := c.AuditLoginTotpWrite
		if r.Method == http.MethodDelete {
			action = c.AuditLoginTotpClear
		}
		err = h.audit(r.Context(), p, requestID, action, id)
	}
	return status, 200, err
}
