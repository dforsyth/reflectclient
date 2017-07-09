package reflectclient

import (
	"net/http"
)

type RequestTransformer func(r *http.Request) *http.Request
