package authz

import "encoding/json"

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }

func jsonUnmarshal(data []byte, target any) error { return json.Unmarshal(data, target) }
