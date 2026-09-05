package circuitstore

import "time"

func timeNowUnixMilli() int64 { return time.Now().UnixMilli() }
