package statsverify

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// ClientIPRegistryBucketCount mirrors clientIpRegistryBucketCount
// (storage/client-ip-normalization.ts, line 4) and
// gatewayclientip.ClientIPRegistryBucketCount.
const ClientIPRegistryBucketCount = 4096

// NormalizedClientIP mirrors NormalizedClientIp
// (storage/client-ip-normalization.ts). Only IPv4 identities survive stats
// normalization: IPv6 and IPv4-in-IPv6 inputs return nil, matching both the
// Node stats writer and gatewayclientip.NormalizeClientIPForStats.
type NormalizedClientIP struct {
	ClientIP       string
	AggregateIPKey string
	IPVersion      int
	IPHash         string
	BucketNo       int
}

var (
	ipv4WithPortPattern = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}:\d+$`)
	ipv4HashPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// NormalizeClientIPForStats mirrors normalizeClientIpForStats
// (storage/client-ip-normalization.ts lines 14-24). The gateway runtime
// package gatewayclientip implements the identical normalization; this
// package keeps a private copy so jobs stay a standalone module.
func NormalizeClientIPForStats(value string) *NormalizedClientIP {
	normalizedIP := normalizePlainClientIP(value)
	if normalizedIP == "" {
		return nil
	}
	// Node isIP(value) === 4. netip.Is4 reports false for IPv6 and
	// IPv4-in-IPv6 forms; the "::ffff:" prefix was already stripped.
	addr, err := netip.ParseAddr(normalizedIP)
	if err != nil || !addr.Is4() {
		return nil
	}
	clientIP := normalizeIpv4(normalizedIP)
	if clientIP == "" {
		return nil
	}
	return clientIPIdentity(clientIP, clientIP, 4)
}

// NormalizeIPHash mirrors normalizeIpHash
// (storage/client-ip-normalization.ts lines 26-29).
func NormalizeIPHash(value string) string {
	text := strings.ToLower(strings.TrimSpace(value))
	if !ipv4HashPattern.MatchString(text) {
		return ""
	}
	return text
}

// normalizePlainClientIP mirrors normalizePlainClientIP
// (storage/client-ip-normalization.ts lines 31-53): trim, first comma
// segment, drop %zone, unwrap [brackets], strip v4:port, strip ::ffff:,
// lowercase.
func normalizePlainClientIP(value string) string {
	if value == "" {
		return ""
	}
	ip := strings.TrimSpace(value)
	if ip == "" {
		return ""
	}
	if strings.Contains(ip, ",") {
		ip = strings.TrimSpace(strings.SplitN(ip, ",", 2)[0])
	}
	if zoneIndex := strings.Index(ip, "%"); zoneIndex > 0 {
		ip = ip[:zoneIndex]
	}
	if strings.HasPrefix(ip, "[") {
		if end := strings.Index(ip, "]"); end > 0 {
			ip = ip[1:end]
		}
	}
	if ipv4WithPortPattern.MatchString(ip) {
		ip = ip[:strings.LastIndex(ip, ":")]
	}
	if strings.HasPrefix(strings.ToLower(ip), "::ffff:") {
		ip = ip[len("::ffff:"):]
	}
	return strings.ToLower(ip)
}

// normalizeIpv4 mirrors normalizeIpv4
// (storage/client-ip-normalization.ts lines 55-60): re-emit canonical
// dotted-decimal without leading zeros.
func normalizeIpv4(value string) string {
	addr, err := netip.ParseAddr(value)
	if err != nil || !addr.Is4() {
		return ""
	}
	octets := addr.As4()
	parts := make([]string, 0, 4)
	for _, octet := range octets {
		parts = append(parts, strconv.Itoa(int(octet)))
	}
	return strings.Join(parts, ".")
}

// clientIPIdentity mirrors clientIpIdentity
// (storage/client-ip-normalization.ts lines 62-71):
// ipHash = sha256("client-ip:" + aggregateIpKey),
// bucketNo = parseInt(ipHash[0:8], 16) % 4096.
func clientIPIdentity(clientIP string, aggregateIPKey string, ipVersion int) *NormalizedClientIP {
	sum := sha256.Sum256([]byte("client-ip:" + aggregateIPKey))
	ipHash := hex.EncodeToString(sum[:])
	bucket, err := strconv.ParseInt(ipHash[:8], 16, 64)
	if err != nil {
		bucket = 0
	}
	return &NormalizedClientIP{
		ClientIP:       clientIP,
		AggregateIPKey: aggregateIPKey,
		IPVersion:      ipVersion,
		IPHash:         ipHash,
		BucketNo:       int(bucket % ClientIPRegistryBucketCount),
	}
}
