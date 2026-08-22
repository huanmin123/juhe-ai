// Package ownermode defines the fail-closed blue/green owner state for Go sidecars.
package ownermode

import (
	"fmt"
	"strings"
)

const EnvironmentKey = "JUHE_AI_BLUE_GREEN_OWNER_MODE"

type Mode string

const (
	Active  Mode = "active"
	Standby Mode = "standby"
	Drain   Mode = "drain"
)

// Load keeps existing single-slot deployments active while requiring every
// non-default value to be one of the explicit blue/green states.
func Load(getenv func(string) string) (Mode, error) {
	value := strings.ToLower(strings.TrimSpace(getenv(EnvironmentKey)))
	switch Mode(value) {
	case "", Active:
		return Active, nil
	case Standby:
		return Standby, nil
	case Drain:
		return Drain, nil
	default:
		return "", fmt.Errorf("%s must be active, standby, or drain", EnvironmentKey)
	}
}

func (mode Mode) OwnsWork() bool {
	return mode == Active
}
