package reflectclient

type RetryHandler interface {
	Retry(error) error
}

type BasicRetryHandler struct {
	maxRetries int
	retryCount int
}

func NewBasicRetryHandler(maxRetries int) *BasicRetryHandler {
	return &BasicRetryHandler{maxRetries, 0}
}

func (h *BasicRetryHandler) Retry(err error) error {
	if h.retryCount < h.maxRetries {
		return nil
	}
	return err
}
