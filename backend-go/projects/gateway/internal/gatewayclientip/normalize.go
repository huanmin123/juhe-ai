package gatewayclientip

import (
	"crypto/sha256"
	"encoding/hex"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// ClientIPRegistryBucketCount mirrors clientIpRegistryBucketCount.
const ClientIPRegistryBucketCount = 4096

// NormalizedClientIP mirrors NormalizedClientIp (storage/client-ip-normalization.ts).
type NormalizedClientIP struct {
	ClientIP       string `json:"clientIp"`
	AggregateIPKey string `json:"aggregateIpKey"`
	IPVersion      int    `json:"ipVersion"`
	IPHash         string `json:"ipHash"`
	BucketNo       int    `json:"bucketNo"`
}

var ipv4WithPortPattern = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}:\d+$`)

// NormalizeClientIPForStats mirrors normalizeClientIpForStats: only IPv4
// identities survive normalization; everything else returns nil.
func NormalizeClientIPForStats(value string) *NormalizedClientIP {
	normalizedIP := normalizePlainClientIP(value)
	if normalizedIP == "" {
		return nil
	}
	// Node isIP(value) === 4: netip.Is4 already reports false for IPv6 and
	// IPv4-in-IPv6 forms; the "::ffff:" prefix was stripped above.
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

// NormalizeIPHashForRuntime mirrors normalizeIpHash (client-ip-normalization.ts).
func NormalizeIPHashForRuntime(value string) string {
	text := strings.ToLower(strings.TrimSpace(value))
	if !ipv4HashPattern.MatchString(text) {
		return ""
	}
	return text
}

var ipv4HashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// normalizePlainClientIP mirrors normalizePlainClientIP.
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

// normalizeIpv4 mirrors normalizeIpv4.
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

// clientIpIdentity mirrors clientIpIdentity: sha256("client-ip:" + aggregate).
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
