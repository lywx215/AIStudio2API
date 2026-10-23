package core

import "net/http"

func (e *Engine) leaseClient(a *lease) *http.Client {
	if a.client != nil {
		return a.client
	}
	return e.client
}

// Generation may prepare a large document before sending response headers.
// Its existing lease context bounds the complete request. Authentication and
// upload keep their shorter header deadline; a slow model is not replayed.
func generationClient(base *http.Client) (*http.Client, func()) {
	transport, ok := base.Transport.(*http.Transport)
	if !ok {
		return base, func() {}
	}
	cloned := transport.Clone()
	cloned.ResponseHeaderTimeout = 0
	client := *base
	client.Transport = cloned
	return &client, cloned.CloseIdleConnections
}
