package legacybusinessimport

import "time"

// timeNowUTC renders the canonical timestamp text the schema stores in every
// text-typed timestamp column.
func timeNowUTC() string { return time.Now().UTC().Format(time.RFC3339Nano) }
