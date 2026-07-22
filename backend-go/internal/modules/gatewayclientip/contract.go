// Package gatewayclientip resolves a gateway client identity without depending
// on net/http, persistence, or a concrete policy cache.
package gatewayclientip

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

const (
	ClientIPRegistryBucketCount = 4096
	MaxForwardedHops            = 64
	maxForwardedHopBytes        = 128
)

type Input struct {
	RemoteAddress string
	ForwardedFor  []string
}

type Trust struct {
	Enabled bool
	All     bool
	Hops    int
}

type Source string

const (
	SourceUnknown      Source = "unknown"
	SourceRemote       Source = "remote"
	SourceForwardedFor Source = "x_forwarded_for"
)

type ForwardedHop struct {
	Raw     string
	Address netip.Addr
	Valid   bool
}

type Resolution struct {
	ClientIP          netip.Addr
	RemoteIP          netip.Addr
	Source            Source
	ForwardedChain    []ForwardedHop
	ForwardedTrusted  bool
	ForwardedRejected bool
}

// Resolve follows Express's trust-proxy hop direction: forwarded values are
// ordered from the original client to the nearest proxy, and numeric trust is
// counted from the socket peer toward the client. Invalid hops retain their
// position so they cannot shift a more distant address into a trusted slot.
func Resolve(input Input, trust Trust) Resolution {
	remoteIP, _ := parseAddress(input.RemoteAddress)
	forwarded, rejected := parseForwardedChain(input.ForwardedFor)
	result := Resolution{
		ClientIP:          remoteIP,
		RemoteIP:          remoteIP,
		Source:            sourceForRemote(remoteIP),
		ForwardedChain:    forwarded,
		ForwardedRejected: rejected,
	}
	if !trust.Enabled || rejected || len(forwarded) == 0 {
		return result
	}

	selected := -1
	if trust.All {
		selected = 0
	} else if trust.Hops > 0 {
		selected = len(forwarded) - trust.Hops
		if selected < 0 {
			selected = 0
		}
	}
	if selected < 0 || selected >= len(forwarded) || !forwarded[selected].Valid {
		return result
	}

	result.ClientIP = forwarded[selected].Address
	result.Source = SourceForwardedFor
	result.ForwardedTrusted = true
	return result
}

func sourceForRemote(address netip.Addr) Source {
	if address.IsValid() {
		return SourceRemote
	}
	return SourceUnknown
}

func parseForwardedChain(values []string) ([]ForwardedHop, bool) {
	hops := make([]ForwardedHop, 0, min(len(values), MaxForwardedHops))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			raw := strings.TrimSpace(part)
			if raw == "" {
				continue
			}
			if len(hops) == MaxForwardedHops {
				return hops, true
			}
			address, valid := parseAddress(raw)
			hops = append(hops, ForwardedHop{
				Raw:     boundedHopText(raw),
				Address: address,
				Valid:   valid,
			})
		}
	}
	return hops, false
}

func boundedHopText(value string) string {
	if len(value) <= maxForwardedHopBytes {
		return value
	}
	return value[:maxForwardedHopBytes]
}

func parseAddress(value string) (netip.Addr, bool) {
	text := strings.TrimSpace(value)
	if text == "" || len(text) > maxForwardedHopBytes {
		return netip.Addr{}, false
	}

	if host, port, err := net.SplitHostPort(text); err == nil {
		if !validPort(port) {
			return netip.Addr{}, false
		}
		text = host
	} else if strings.HasPrefix(text, "[") {
		end := strings.IndexByte(text, ']')
		if end <= 0 || strings.TrimSpace(text[end+1:]) != "" {
			return netip.Addr{}, false
		}
		text = text[1:end]
	} else if strings.Count(text, ":") == 1 {
		host, port, found := strings.Cut(text, ":")
		if found && validPort(port) {
			text = host
		}
	}

	if zone := strings.LastIndexByte(text, '%'); zone > 0 {
		text = text[:zone]
	}
	address, err := netip.ParseAddr(strings.TrimSpace(text))
	if err != nil {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func validPort(value string) bool {
	port, err := strconv.Atoi(value)
	return err == nil && port >= 0 && port <= 65535
}

type Identity struct {
	Address netip.Addr
}

type PolicyLookup struct {
	ClientIP       string
	AggregateIPKey string
	IPVersion      int
	IPHash         string
	BucketNo       int
}

// PolicyLookup returns the Node-compatible key used by the current client IP
// policy caches and stores. Current policy data is IPv4-only by design.
func (i Identity) PolicyLookup() (PolicyLookup, bool) {
	address := i.Address.Unmap()
	if !address.IsValid() || !address.Is4() {
		return PolicyLookup{}, false
	}
	clientIP := address.String()
	sum := sha256.Sum256([]byte("client-ip:" + clientIP))
	ipHash := hex.EncodeToString(sum[:])
	bucketPrefix := binary.BigEndian.Uint32(sum[:4])
	return PolicyLookup{
		ClientIP:       clientIP,
		AggregateIPKey: clientIP,
		IPVersion:      4,
		IPHash:         ipHash,
		BucketNo:       int(bucketPrefix % ClientIPRegistryBucketCount),
	}, true
}

type PolicyInput struct {
	Identity    Identity
	Allowlisted bool
	Blacklisted bool
}

type PolicyReason string

const (
	PolicyReasonNone          PolicyReason = "none"
	PolicyReasonUnknownClient PolicyReason = "unknown_client"
	PolicyReasonAllowlisted   PolicyReason = "allowlisted"
	PolicyReasonBlacklisted   PolicyReason = "blacklisted"
)

type PolicyDecision struct {
	ClientIP    netip.Addr
	Allowlisted bool
	Blocked     bool
	Reason      PolicyReason
}

// DecidePolicy consumes already-resolved cache/store matches. Blacklist wins
// if inconsistent upstream state reports both policy types, while allowlist is
// only an annotation/bypass input and never overrides a gateway block.
func DecidePolicy(input PolicyInput) PolicyDecision {
	decision := PolicyDecision{ClientIP: input.Identity.Address}
	if _, ok := input.Identity.PolicyLookup(); !ok {
		decision.Reason = PolicyReasonUnknownClient
		return decision
	}
	if input.Blacklisted {
		decision.Blocked = true
		decision.Reason = PolicyReasonBlacklisted
		return decision
	}
	if input.Allowlisted {
		decision.Allowlisted = true
		decision.Reason = PolicyReasonAllowlisted
		return decision
	}
	decision.Reason = PolicyReasonNone
	return decision
}
