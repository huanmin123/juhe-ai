package statreads

import "encoding/json"

func jsonUnmarshalHelper(body []byte, target any) error { return json.Unmarshal(body, target) }
