package grpcjson

import (
	"encoding/json"

	"google.golang.org/grpc/encoding"
)

type Codec struct{}

func (Codec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func (Codec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func (Codec) Name() string {
	return "json"
}

func Register() {
	encoding.RegisterCodec(Codec{})
}
