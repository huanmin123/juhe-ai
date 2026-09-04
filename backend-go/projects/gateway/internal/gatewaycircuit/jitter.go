package gatewaycircuit

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"math"
)

// Passive schedule jitter policy mirrors shared/passive-schedule-jitter.ts.
// Passive polling and periodic scans must use it so same-phase fleets never
// converge; lease/ownership deadlines never do.
const (
	passiveScheduleSubMinuteWindowMs = int64(30_000)
	passiveScheduleMinuteWindowMs    = int64(30_000)
	passiveScheduleHourWindowMs      = int64(30 * 60_000)
	passiveScheduleDayWindowMs       = int64(60 * 60_000)
	passiveScheduleWeekWindowMs      = int64(8 * 60 * 60_000)
)

func truncMs(value float64) int64 {
	return int64(math.Trunc(value))
}

// passiveScheduleJitterWindowMs mirrors passiveScheduleJitterWindowMs.
func passiveScheduleJitterWindowMs(intervalMs int64) int64 {
	interval := int64(1)
	if raw := truncMs(float64(intervalMs)); raw >= 1 {
		interval = raw
	}
	var windowMs int64
	switch {
	case interval < 60_000:
		windowMs = passiveScheduleSubMinuteWindowMs
		if half := interval / 2; half < windowMs {
			windowMs = half
		}
	case interval < 60*60_000:
		windowMs = passiveScheduleMinuteWindowMs
	case interval < 24*60*60_000:
		windowMs = passiveScheduleHourWindowMs
	case interval < 7*24*60*60_000:
		windowMs = passiveScheduleDayWindowMs
	default:
		windowMs = passiveScheduleWeekWindowMs
	}
	half := interval / 2
	if half < 0 {
		half = 0
	}
	if windowMs > half {
		return half
	}
	return windowMs
}

// passiveScheduleOffsetWithinWindowMs mirrors passiveScheduleOffsetWithinWindowMs.
func passiveScheduleOffsetWithinWindowMs(windowMs int64, random func() float64) int64 {
	if windowMs <= 0 {
		return 0
	}
	sampled := random()
	unit := 0.0
	if !math.IsNaN(sampled) && !math.IsInf(sampled, 0) {
		unit = math.Min(1, math.Max(0, sampled))
	}
	offset := int64(math.Min(float64(windowMs), math.Floor(unit*float64(windowMs*2+1)))) - windowMs
	if offset == 0 {
		return 1
	}
	return offset
}

// passiveScheduleDelayMs mirrors passiveScheduleDelayMs: adds a fresh offset
// while keeping a strictly positive delay.
func passiveScheduleDelayMs(intervalMs int64, random func() float64) int64 {
	base := truncMs(float64(intervalMs))
	if !(base >= 1) {
		base = 1
	}
	return int64(math.Max(1, float64(base+passiveScheduleOffsetMs(intervalMs, random))))
}

func passiveScheduleOffsetMs(intervalMs int64, random func() float64) int64 {
	return passiveScheduleOffsetWithinWindowMs(passiveScheduleJitterWindowMs(intervalMs), random)
}

// passiveScheduleNotBeforeDelayMs mirrors passiveScheduleNotBeforeDelayMs: a
// fresh delay at or after a hard external deadline.
func passiveScheduleNotBeforeDelayMs(intervalMs int64, random func() float64) int64 {
	interval := truncMs(float64(intervalMs))
	if !(interval >= 1) {
		interval = 1
	}
	offset := passiveScheduleOffsetMs(interval, random)
	if offset == 0 {
		return interval + 1
	}
	if offset < 0 {
		offset = -offset
	}
	return interval + offset
}

// sha1Hex returns the lowercase hex SHA-1 digest (Node createHash('sha1')).
func sha1Hex(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

// sha256Hex returns the lowercase hex SHA-256 digest.
func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// accountCircuitBackoffDelayMs mirrors accountCircuitBackoffDelayMs in
// account-circuit-store.ts: early attempts are exact, later attempts carry a
// deterministic symmetric offset when a seed is supplied (Redis and memory
// stores derive the same deadline from the same capability/state identity) or
// a fresh random offset otherwise.
func (s Settings) accountCircuitBackoffDelayMs(attempt int64, jitterSeed string, random func() float64) int64 {
	backoff := s.AccountCircuitBackoffMs
	if len(backoff) == 0 {
		backoff = DefaultSettings().AccountCircuitBackoffMs
	}
	index := truncMs(float64(attempt)) - 1
	if index < 0 {
		index = 0
	}
	if index > int64(len(backoff)-1) {
		index = int64(len(backoff) - 1)
	}
	base := backoff[index]
	if index < 4 {
		return base
	}
	if jitterSeed != "" {
		windowMs := passiveScheduleJitterWindowMs(base)
		if windowMs <= 0 {
			return base
		}
		digest := sha1Hex(jitterSeed)
		sample := hexPrefixSample(digest)
		offset := int64(sample%uint64(windowMs*2+1)) - windowMs
		if offset == 0 {
			offset = 1
		}
		delay := base + offset
		if delay < 1 {
			return 1
		}
		return delay
	}
	if random == nil {
		random = defaultRandom
	}
	return passiveScheduleDelayMs(base, random)
}

// hexPrefixSample mirrors Number.parseInt(digest.slice(0, 8), 16).
func hexPrefixSample(digest string) uint64 {
	prefix := digest
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	sample := uint64(0)
	for i := 0; i < len(prefix); i++ {
		digit := uint64(0)
		c := prefix[i]
		switch {
		case c >= '0' && c <= '9':
			digit = uint64(c - '0')
		case c >= 'a' && c <= 'f':
			digit = uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			digit = uint64(c-'A') + 10
		default:
			return sample // parseInt stops at the first invalid digit
		}
		sample = sample*16 + digit
	}
	return sample
}
