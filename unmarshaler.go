package reflectclient

import (
	"encoding/json"
)

type Unmarshaler interface {
	Unmarshal([]byte, interface{}) error
}

type JsonUnmarshaler struct {
}

func (u *JsonUnmarshaler) Unmarshal(in []byte, obj interface{}) error {
	return json.Unmarshal(in, obj)
}
