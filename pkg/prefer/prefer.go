package prefer

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"edgetunnel-lb/pkg/config"
	"edgetunnel-lb/pkg/dialer"
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
		m.SetDefaultTarget(cfg.DefaultPrefer)
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

func (m *Manager) getOrCreateNodeLocked(target string, isDefault ...bool) (*model.PreferNode, string) {
	host, port := ParseTargetHostPort(target)
	key := fmt.Sprintf("%s:%d", host, port)

	node, exists := m.nodes[key]
	if !exists {
		node = &model.PreferNode{
			Target:  target,
			Host:    host,
			Port:    port,
			Clients: make([]string, 0, 5),
		}
		m.nodes[key] = node
	}

	if len(isDefault) > 0 && isDefault[0] {
		node.IsDefault = true
	}
	return node, key
}

// RecordRequest 记录真实客户端入站请求（累加请求计数，记录来源客户端 IP 与活跃时间）
func (m *Manager) RecordRequest(target string, clientIP string, isDefault ...bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	node, _ := m.getOrCreateNodeLocked(target, isDefault...)
	node.RequestCount++

	now := time.Now()
	node.LastSeen = now.UnixMilli()
	node.LastSeenTime = now.Format("15:04:05")

	// 处理客户端来源 IP（去端口、去重、最多保留 5 个最近活跃设备）
	if clientIP != "" {
		pureIP := clientIP
		if h, _, err := net.SplitHostPort(clientIP); err == nil {
			pureIP = h
		}
		pureIP = strings.TrimSpace(pureIP)
		if pureIP != "" && pureIP != "127.0.0.1" && pureIP != "::1" && pureIP != "localhost" {
			found := false
			for _, c := range node.Clients {
				if c == pureIP {
					found = true
					break
				}
			}
			if !found {
				if len(node.Clients) >= 5 {
					node.Clients = node.Clients[1:]
				}
				node.Clients = append(node.Clients, pureIP)
			}
		}
	}
}

// RecordResponse 记录请求返回状态与延迟
func (m *Manager) RecordResponse(target string, latencyMs int, status, reason string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	node, _ := m.getOrCreateNodeLocked(target)
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

// RecordPing 记录测速结果 (仅更新延迟与连通状态，绝不计入业务请求数)
func (m *Manager) RecordPing(target string, latencyMs int, status, reason string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	node, _ := m.getOrCreateNodeLocked(target)
	if latencyMs > 0 {
		node.LastLatency = latencyMs
	}
	if status != "" {
		node.LastStatus = status
	}
	if reason != "" {
		node.LastReason = reason
	}
}

// SetDefaultTarget 切换或标记默认兜底节点
func (m *Manager) SetDefaultTarget(target string) {
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	host, port := ParseTargetHostPort(target)
	newDefKey := fmt.Sprintf("%s:%d", host, port)

	for k, n := range m.nodes {
		if k == newDefKey {
			n.IsDefault = true
		} else {
			n.IsDefault = false
		}
	}

	node, _ := m.getOrCreateNodeLocked(target, true)
	node.IsDefault = true
}

// RecordActivity 兼容旧接口
func (m *Manager) RecordActivity(target string, latencyMs int, status, reason string, isDefault ...bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	node, _ := m.getOrCreateNodeLocked(target, isDefault...)
	node.RequestCount++

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

	// 规则 A 稳定排序：
	// 1. 默认兜底节点始终排在第 1 位
	// 2. 业务请求量 (RequestCount) 从大到小降序
	// 3. 请求量相同时按 Target 字典序稳定排列，防止刷新时跳动
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].IsDefault != list[j].IsDefault {
			return list[i].IsDefault
		}
		if list[i].RequestCount != list[j].RequestCount {
			return list[i].RequestCount > list[j].RequestCount
		}
		return list[i].Target < list[j].Target
	})

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
	conn, err := dialer.DialTimeout("tcp", addr, timeout)
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
