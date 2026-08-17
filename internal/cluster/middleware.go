package cluster

import (
	"net"
	"net/http"
	"strings"
)

// IsInternalIP 检查 IP 是否为内网地址
func IsInternalIP(ip string) bool {
	// 移除端口号
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	
	// 私有地址范围
	privateRanges := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",
	}
	
	// Docker 网络地址
	dockerRanges := []string{
		"172.17.0.0/16",
		"172.18.0.0/16",
		"172.19.0.0/16",
	}
	
	// 检查是否为内网地址
	for _, cidr := range append(privateRanges, dockerRanges...) {
		_, network, _ := net.ParseCIDR(cidr)
		if network != nil && network.Contains(net.ParseIP(ip)) {
			return true
		}
	}
	
	// 检查是否为 localhost
	if ip == "localhost" || ip == "127.0.0.1" || ip == "::1" {
		return true
	}
	
	return false
}

// InternalOnlyMiddleware 仅允许内网访问
func InternalOnlyMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 获取客户端 IP
		clientIP := r.RemoteAddr
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// 取第一个 IP
			if idx := strings.Index(xff, ","); idx > 0 {
				clientIP = xff[:idx]
			} else {
				clientIP = xff
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			clientIP = xri
		}
		
		// 检查是否为内网
		if !IsInternalIP(clientIP) {
			http.Error(w, `{"error":"forbidden: internal only"}`, http.StatusForbidden)
			return
		}
		
		next.ServeHTTP(w, r)
	})
}
