package prefer

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"edgetunnel-lb/pkg/config"
	"edgetunnel-lb/pkg/model"
)

type Manager struct {
	mu     sync.RWMutex
	nodes  map[string]*model.PreferNode
	cfgMgr *config.Manager
}

var globalManager *Manager

func InitPreferManager(cfgMgr *config.Manager) *Manager {
	m := &Manager{
		nodes:  make(map[string]*model.PreferNode),
		cfgMgr: cfgMgr,
	}
	globalManager = m

	cfg := cfgMgr.Get()
	if cfg.DefaultPrefer != "" {
		m.RecordActivity(cfg.DefaultPrefer, 0, "ok", "系统启动初始化", true)
	}

	return m
}

func GetManager() *Manager {
	return globalManager
}

// ParseTargetHostPort splits target into host and port, defaulting to 443
func ParseTargetHostPort(target string) (string, int) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "127.0.0.1", 443
	}

	// Handle [IPv6]:port or [IPv6]
	if strings.HasPrefix(target, "[") {
		idx := strings.LastIndex(target, "]")
		if idx != -1 {
			host := target[1:idx]
			rest := target[idx+1:]
			if strings.HasPrefix(rest, ":") {
				if p, err := strconv.Atoi(rest[1:]); err == nil && p > 0 {
					return host, p
				}
			}
			return host, 443
		}
	}

	// IPv4 or domain:port
	if strings.Contains(target, ":") {
		parts := strings.Split(target, ":")
		if len(parts) == 2 {
			if p, err := strconv.Atoi(parts[1]); err == nil && p > 0 {
				return parts[0], p
			}
		}
	}

	return target, 443
}

var (
	preferRegex    = regexp.MustCompile(`(?i)[?&]prefer=([^&/]+)`)
	cleanPreferReg = regexp.MustCompile(`(?i)[?&]prefer=[^&/]+`)
	cleanQA        = regexp.MustCompile(`\?&`)
	cleanAA        = regexp.MustCompile(`&&+`)
	cleanTail      = regexp.MustCompile(`[?&]$`)
)

// ParsePreferAndCleanPath extracts prefer= target from request path and returns target and cleaned path
func ParsePreferAndCleanPath(rawPath string, defaultPrefer string) (string, string) {
	if rawPath == "" {
		return defaultPrefer, "/"
	}

	target := ""
	matches := preferRegex.FindStringSubmatch(rawPath)
	if len(matches) > 1 {
		decoded := matches[1]
		for i := 0; i < 3; i++ {
			if !strings.Contains(decoded, "%") {
				break
			}
			if unesc, err := url.QueryUnescape(decoded); err == nil && unesc != decoded {
				decoded = unesc
			} else {
				break
			}
		}
		target = strings.TrimSpace(decoded)
	}

	if target == "" {
		target = defaultPrefer
	}

	cleaned := cleanPreferReg.ReplaceAllString(rawPath, "")
	cleaned = cleanQA.ReplaceAllString(cleaned, "?")
	cleaned = cleanAA.ReplaceAllString(cleaned, "&")
	cleaned = cleanTail.ReplaceAllString(cleaned, "")
	cleaned = strings.TrimRight(cleaned, "/")

	if cleaned == "" {
		cleaned = "/"
	}

	return target, cleaned
}

func (m *Manager) RecordActivity(target string, latencyMs int, status, reason string, isDefault ...bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}

	host, port := ParseTargetHostPort(target)
	key := fmt.Sprintf("%s:%d", host, port)

	m.mu.Lock()
	defer m.mu.Unlock()

	node, exists := m.nodes[key]
	if !exists {
		node = &model.PreferNode{
			Target: target,
			Host:   host,
			Port:   port,
		}
		m.nodes[key] = node
	}

	if len(isDefault) > 0 && isDefault[0] {
		node.IsDefault = true
	}

	now := time.Now()
	node.LastSeen = now.UnixMilli()
	node.LastSeenTime = now.Format("15:04:05")

	if latencyMs > 0 {
		node.LastLatency = latencyMs
	}
	if status != "" {
		node.LastStatus = status
	}
	if reason != "" {
		node.LastReason = reason
	}

	switch status {
	case "ok":
		node.SuccessReqs++
	case "error":
		node.FailReqs++
	}
}

func (m *Manager) GetNodesList() []model.PreferNode {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]model.PreferNode, 0, len(m.nodes))
	for _, n := range m.nodes {
		list = append(list, *n)
	}
	return list
}

func (m *Manager) ClearNonDefault() {
	m.mu.Lock()
	defer m.mu.Unlock()

	cfg := m.cfgMgr.Get()
	defHost, defPort := ParseTargetHostPort(cfg.DefaultPrefer)
	defKey := fmt.Sprintf("%s:%d", defHost, defPort)

	for k, n := range m.nodes {
		if !n.IsDefault && k != defKey {
			delete(m.nodes, k)
		}
	}
}

type PingResult struct {
	Target  string `json:"target"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Latency int    `json:"latency"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

func PingTarget(target string, timeout time.Duration) PingResult {
	host, port := ParseTargetHostPort(target)
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	start := time.Now()
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return PingResult{
			Target: target,
			Host:   host,
			Port:   port,
			Status: "error",
			Error:  err.Error(),
		}
	}
	defer conn.Close()

	// If port is 443 or TLS, do quick TLS handshake verification
	if port == 443 {
		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         "cloudflare.com",
			InsecureSkipVerify: true,
		})
		_ = tlsConn.SetDeadline(time.Now().Add(timeout))
		if err := tlsConn.Handshake(); err != nil {
			return PingResult{
				Target: target,
				Host:   host,
				Port:   port,
				Status: "error",
				Error:  fmt.Sprintf("TLS 握手失败: %v", err),
			}
		}
	}

	latency := int(time.Since(start).Milliseconds())
	return PingResult{
		Target:  target,
		Host:    host,
		Port:    port,
		Latency: latency,
		Status:  "ok",
	}
}
