package host

// Principal is invoked after existing model-key authentication. Disabled or
// damaged modules return an inert scope so an ordinary read cannot initialize
// storage; their public handler rejects the request before accessing resources.
func (h *Host) Principal(modelKey string) (string, error) {
	h.mu.RLock()
	active := h.config.Enabled && h.problem == "" && !h.closed.Load()
	h.mu.RUnlock()
	if !active {
		return "business-disabled", nil
	}
	return h.accounts.Principal(modelKey)
}
