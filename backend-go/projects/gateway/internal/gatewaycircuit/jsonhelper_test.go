package gatewaycircuit

import "encoding/json"

func jsonUnmarshal(raw []byte, dst any) error { return json.Unmarshal(raw, dst) }
