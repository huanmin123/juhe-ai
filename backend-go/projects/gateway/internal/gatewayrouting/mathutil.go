package gatewayrouting

import "math"

// negInf mirrors Number.NEGATIVE_INFINITY in the priority comparisons.
func negInf() float64 { return math.Inf(-1) }
