package dialer

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"edgetunnel-lb/pkg/logger"
)

// 默认纯净出站 DNS 服务器列表（直接面向公网）
var defaultPublicDNS = []string{
	"223.5.5.5:53",   // 阿里公共 DNS (默认)
	"119.29.29.29:53", // 腾讯 DNSPod (默认)
}

type cacheEntry struct {
	ips       []string
	expiresAt time.Time
}

type CleanResolver struct {
	mu        sync.RWMutex
	cache     map[string]cacheEntry
	publicDNS []string
	ttl       time.Duration
}

var (
	defaultResolverOnce sync.Once
	defaultResolver     *CleanResolver
)

// GetDefaultResolver 获取全局单例纯净出站 DNS 解析器
func GetDefaultResolver() *CleanResolver {
	defaultResolverOnce.Do(func() {
		defaultResolver = &CleanResolver{
			cache:     make(map[string]cacheEntry),
			publicDNS: defaultPublicDNS,
			ttl:       2 * time.Minute, // 缓存 2 分钟，兼顾高并发与动态 IP 刷新
		}
	})
	return defaultResolver
}

// SetDNS 动态更新出站纯净 DNS 服务器列表
func SetDNS(dnsList []string) {
	r := GetDefaultResolver()
	r.mu.Lock()
	defer r.mu.Unlock()

	var normalized []string
	for _, item := range dnsList {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, ":") {
			item = item + ":53"
		}
		normalized = append(normalized, item)
	}

	if len(normalized) == 0 {
		normalized = defaultPublicDNS
	}

	r.publicDNS = normalized
	// 清空历史缓存以立即生效
	r.cache = make(map[string]cacheEntry)
	logger.Infof("[DNS] 🔄 网关出站纯净 DNS 服务器已更新为: %v", r.publicDNS)
}

// GetDNS 获取当前生效的出站 DNS 列表
func GetDNS() []string {
	r := GetDefaultResolver()
	r.mu.RLock()
	defer r.mu.RUnlock()
	res := make([]string, len(r.publicDNS))
	copy(res, r.publicDNS)
	return res
}

// Resolve 纯净解析域名：直接向配置的纯净 DNS 服务器发起查询，彻底绕过本机 DNS/Fake-IP
func (r *CleanResolver) Resolve(ctx context.Context, host string) ([]string, error) {
	// 1. 如果本身已经是 IP 地址，直接返回
	if ip := net.ParseIP(host); ip != nil {
		return []string{host}, nil
	}

	hostLower := strings.ToLower(strings.TrimSpace(host))

	// 2. 检查内存缓存，避免高并发转发时频繁发起外部 UDP DNS 请求
	r.mu.RLock()
	if entry, found := r.cache[hostLower]; found {
		if time.Now().Before(entry.expiresAt) && len(entry.ips) > 0 {
			r.mu.RUnlock()
			return entry.ips, nil
		}
	}
	dnsServers := make([]string, len(r.publicDNS))
	copy(dnsServers, r.publicDNS)
	r.mu.RUnlock()

	// 3. 依次向配置的纯净 DNS 服务器发起 UDP 53 解析
	var lastErr error
	for _, dnsServer := range dnsServers {
		netRes := &net.Resolver{
			PreferGo: true,
			Dial: func(dialCtx context.Context, network, address string) (net.Conn, error) {
				d := net.Dialer{Timeout: 3 * time.Second}
				return d.DialContext(dialCtx, "udp", dnsServer)
			},
		}

		cleanIPs, dnsErr := netRes.LookupIPAddr(ctx, hostLower)
		if dnsErr == nil && len(cleanIPs) > 0 {
			var cleanList []string
			for _, cip := range cleanIPs {
				cleanList = append(cleanList, cip.IP.String())
			}
			if len(cleanList) > 0 {
				logger.Infof("[DNS] ✅ 域名 %s 通过纯净 DNS (%s) 成功解析: %v", host, dnsServer, cleanList)
				r.updateCache(hostLower, cleanList)
				return cleanList, nil
			}
		}
		lastErr = dnsErr
	}

	if lastErr != nil {
		return nil, fmt.Errorf("解析域名 %s 失败 (纯净 DNS %v 均无法解析): %w", host, dnsServers, lastErr)
	}
	return nil, fmt.Errorf("解析域名 %s 失败: 未返回任何有效 IP", host)
}

// ResolveClean 兼容保留 ResolveClean 别名
func (r *CleanResolver) ResolveClean(ctx context.Context, host string) ([]string, error) {
	return r.Resolve(ctx, host)
}

func (r *CleanResolver) updateCache(host string, ips []string) {
	if len(ips) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache[host] = cacheEntry{
		ips:       ips,
		expiresAt: time.Now().Add(r.ttl),
	}
}


// DialContext 核心纯净拨号器（满足标准 http.Transport.DialContext 签名）：自动完成 anti-fakeip 域名解析并建立 TCP 连接
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return DialContextWithTimeout(ctx, network, addr, 10*time.Second)
}

// DialContextWithTimeout 带自定义超时时限的纯净拨号器
func DialContextWithTimeout(ctx context.Context, network, addr string, timeout time.Duration) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
		port = "443"
	}

	resolver := GetDefaultResolver()
	ips, err := resolver.ResolveClean(ctx, host)
	if err != nil {
		// 解析异常时回退到常规直连拨号
		d := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, network, addr)
	}

	dialer := net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
	}

	var lastErr error
	for _, ipStr := range ips {
		targetAddr := net.JoinHostPort(ipStr, port)
		conn, dialErr := dialer.DialContext(ctx, network, targetAddr)
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}

	return nil, fmt.Errorf("连接目标 %s (%v) 失败: %w", addr, ips, lastErr)
}

// DialTimeout 快速超时拨号封装 (兼容 net.DialTimeout)
func DialTimeout(network, addr string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return DialContextWithTimeout(ctx, network, addr, timeout)
}

// NewCleanHTTPTransport 构建防 Fake-IP 污染的 HTTP Transport，如果配置了代理则交由代理，否则启用纯净直连
func NewCleanHTTPTransport(proxyURL string, timeout time.Duration) *http.Transport {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         DialContext,
		TLSHandshakeTimeout: timeout,
	}

	if proxyURL != "" {
		if u, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(u)
			// 配置了专属代理时由代理接管解析与转发
			transport.DialContext = (&net.Dialer{
				Timeout:   timeout,
				KeepAlive: 30 * time.Second,
			}).DialContext
		}
	}

	return transport
}

// NewCleanHTTPClient 构建全局统一的防 Fake-IP 污染 HTTP 客户端
func NewCleanHTTPClient(proxyURL string, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: NewCleanHTTPTransport(proxyURL, timeout),
		Timeout:   timeout,
	}
}
