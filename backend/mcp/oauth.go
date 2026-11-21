package mcp

import (
	"fmt"
	"strings"
)

func authRequiredError(serverID string) error {
	return fmt.Errorf("[MCP_AUTH_REQUIRED] [mcp:%s] 连接器需要登录", serverID)
}

// hasBearerChallenge reports whether any WWW-Authenticate value offers the
// Bearer scheme (RFC 6750 §3), alone or among other challenges.
func hasBearerChallenge(values []string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			fields := strings.Fields(part)
			if len(fields) == 0 {
				continue
			}
			scheme := fields[0]
			if strings.Contains(scheme, "=") {
				continue // an auth-param of the previous challenge
			}
			if strings.EqualFold(scheme, "bearer") {
				return true
			}
		}
	}
	return false
}
