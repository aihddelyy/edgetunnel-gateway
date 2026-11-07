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
	"223.5.5.5:53",    // 阿里公共 DNS (默认)
	"119.29.29.29:53", // 腾讯 DNSPod (默认)
}

type cacheEntry struct {
	ips       []string
	expiresAt time.Time
}

type dnsCall struct {
	wg  sync.WaitGroup
	ips []string
	err error
}

type CleanResolver struct {
	mu        sync.RWMutex
	cache     map[string]cacheEntry
	inflight  map[string]*dnsCall
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
			inflight:  make(map[string]*dnsCall),
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
	r.mu.RUnlock()

	// 2.5 Singleflight 并发合并：同一时刻对同一域名的解析仅发起 1 次网络查询，防缓存击穿与日志刷屏
	r.mu.Lock()
	if entry, found := r.cache[hostLower]; found {
		if time.Now().Before(entry.expiresAt) && len(entry.ips) > 0 {
			r.mu.Unlock()
			return entry.ips, nil
		}
	}
	if r.inflight == nil {
		r.inflight = make(map[string]*dnsCall)
	}
	if call, ok := r.inflight[hostLower]; ok {
		r.mu.Unlock()
		call.wg.Wait()
		return call.ips, call.err
	}
	call := &dnsCall{}
	call.wg.Add(1)
	r.inflight[hostLower] = call
	dnsServers := make([]string, len(r.publicDNS))
	copy(dnsServers, r.publicDNS)
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.inflight, hostLower)
		r.mu.Unlock()
		call.wg.Done()
	}()

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
				call.ips = cleanList
				return cleanList, nil
			}
		}
		lastErr = dnsErr
	}

	if lastErr != nil {
		call.err = fmt.Errorf("解析域名 %s 失败 (纯净 DNS %v 均无法解析): %w", host, dnsServers, lastErr)
		return nil, call.err
	}
	call.err = fmt.Errorf("解析域名 %s 失败: 未返回任何有效 IP", host)
	return nil, call.err
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

	// 防止缓存无限制膨胀（LRU/TTL 周期清理）
	const maxCacheSize = 256
	if len(r.cache) >= maxCacheSize {
		now := time.Now()
		for k, v := range r.cache {
			if now.After(v.expiresAt) {
				delete(r.cache, k)
			}
		}
		if len(r.cache) >= maxCacheSize {
			r.cache = make(map[string]cacheEntry)
		}
	}

	r.cache[host] = cacheEntry{
		ips:       ips,
		expiresAt: time.Now().Add(r.ttl),
	}
}

// DialContext 核心纯净拨号器（满足标准 http.Transport.DialContext 签名）：自动完成 anti-fakeip 域名解析并建立 TCP 连接
func DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return DialContextWithTimeout(ctx, network, addr, 10*time.Second)
}

// DialContextWithTimeout 带自定义超时时限的纯净拨号器（支持多 IP 错峰竞速快速建连）
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

	if len(ips) == 1 {
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
	}

	// 多 IP 错峰竞速拨号 (Happy Eyeballs 机制)：首个 IP 超时 300ms 未建连即并发尝试后续 IP，谁先通先用谁，杜绝串行长超时卡顿
	type dialResult struct {
		conn net.Conn
		err  error
	}

	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	resCh := make(chan dialResult, len(ips))
	remaining := len(ips)
	var lastErr error

	for i, ipStr := range ips {
		targetAddr := net.JoinHostPort(ipStr, port)

		go func(addr string) {
			conn, dErr := dialer.DialContext(dialCtx, network, addr)
			if dErr != nil {
				resCh <- dialResult{err: dErr}
				return
			}
			select {
			case resCh <- dialResult{conn: conn}:
			case <-dialCtx.Done():
				// 已有更快连接抢先胜出，立即关闭冗余连接，杜绝连接泄露
				_ = conn.Close()
			}
		}(targetAddr)

		if i < len(ips)-1 {
			timer := time.NewTimer(300 * time.Millisecond)
			select {
			case res := <-resCh:
				timer.Stop()
				remaining--
				if res.conn != nil {
					return res.conn, nil
				}
				lastErr = res.err
			case <-timer.C:
			case <-dialCtx.Done():
				timer.Stop()
				return nil, dialCtx.Err()
			}
		}
	}

	for remaining > 0 {
		select {
		case res := <-resCh:
			remaining--
			if res.conn != nil {
				return res.conn, nil
			}
			lastErr = res.err
		case <-dialCtx.Done():
			return nil, dialCtx.Err()
		}
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
