package proxyresolve

import (
	"strings"

	"github.com/huanminabc/juhe-ai/backend-go-platform/rediscfg"
)

// MemberHealthKeyPrefix is the shared Redis domain key prefix. jobs writes the
// member health projection there; gateway reads it on the request path. This is
// deliberately not the gateway invalidation-bus key space.
const MemberHealthKeyPrefix = "proxy-pool:member-health:"

// MemberHealthKey returns juhe-ai:{<namespace>}:proxy-pool:member-health:<id>.
// An empty namespace or profile ID returns "" so callers fail closed instead of
// sharing an untagged key across environments.
func MemberHealthKey(namespace, profileID string) string {
	profileID = strings.TrimSpace(profileID)
	namespace = strings.TrimSpace(namespace)
	if profileID == "" || namespace == "" {
		return ""
	}
	return rediscfg.NamespacedKey(MemberHealthKeyPrefix+profileID, namespace)
}
