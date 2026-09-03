package authz

import "encoding/json"

func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }
