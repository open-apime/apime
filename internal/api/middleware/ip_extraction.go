package middleware

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

func GetClientIP(c *gin.Context) string {
	if ip := c.GetHeader("CF-Connecting-IP"); ip != "" {
		if validIP := validateIP(ip); validIP != "" {
			return validIP
		}
	}

	if ips := c.GetHeader("X-Forwarded-For"); ips != "" {
		parts := strings.Split(ips, ",")
		for _, part := range parts {
			if validIP := validateIP(strings.TrimSpace(part)); validIP != "" {
				return validIP
			}
		}
	}

	if ip := c.GetHeader("X-Real-IP"); ip != "" {
		if validIP := validateIP(ip); validIP != "" {
			return validIP
		}
	}

	if ip := c.GetHeader("X-Client-IP"); ip != "" {
		if validIP := validateIP(ip); validIP != "" {
			return validIP
		}
	}

	return c.ClientIP()
}

func validateIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}

	if strings.Contains(ip, ":") {
		parts := strings.Split(ip, ":")
		ip = parts[0]
	}

	if net.ParseIP(ip) != nil {
		return ip
	}

	return ""
}

func IsPrivateIP(ip string) bool {
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.1/32",
		"::1/128",
		"fc00::/7",
	}

	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		return false
	}

	for _, cidr := range privateRanges {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if network.Contains(parsedIP) {
			return true
		}
	}

	return false
}
