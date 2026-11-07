package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"edgetunnel-lb/pkg/config"
	"edgetunnel-lb/pkg/dialer"
	"edgetunnel-lb/pkg/health"
	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/model"
	"edgetunnel-lb/pkg/prefer"
	"edgetunnel-lb/pkg/quota"
	"edgetunnel-lb/pkg/traffic"
)

type transportEntry struct {
	transport *http2.Transport
	lastUsed  time.Time
}

type XHTTPGateway struct {
	cfgMgr    *config.Manager
	healthMgr *health.Manager
	preferMgr *prefer.Manager
	quotaMgr  *quota.Manager
	metrics   *model.Metrics

	server   *http.Server
	listener net.Listener

	transportsMu sync.Mutex
	transports   map[string]*transportEntry
}

func NewXHTTPGateway(cfgMgr *config.Manager, healthMgr *health.Manager, preferMgr *prefer.Manager, quotaMgr *quota.Manager, metrics *model.Metrics) *XHTTPGateway {
	return &XHTTPGateway{
		cfgMgr:     cfgMgr,
		healthMgr:  healthMgr,
		preferMgr:  preferMgr,
		quotaMgr:   quotaMgr,
		metrics:    metrics,
		transports: make(map[string]*transportEntry),
	}
}

func (g *XHTTPGateway) Start() error {
	cfg := g.cfgMgr.Get()
	addr := fmt.Sprintf(":%d", cfg.H2CPort)

	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	g.listener = l

	// Main XHTTP Handler
	mainHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		protoLabel := "XHTTP/H1.1-POST"
		if r.ProtoMajor == 2 {
			protoLabel = "XHTTP/H2C"
			if g.metrics != nil {
				atomic.AddInt64(&g.metrics.H2CRequests, 1)
			}
		} else {
			if g.metrics != nil {
				atomic.AddInt64(&g.metrics.H1Requests, 1)
			}
		}
		if g.metrics != nil {
			atomic.AddInt64(&g.metrics.XHTTPRequests, 1)
		}
		g.handleWorkerForward(w, r, protoLabel)
	})

	h2Server := &http2.Server{
		MaxConcurrentStreams: 500,
		MaxReadFrameSize:     65536,
	}

	g.server = &http.Server{
		Addr:    addr,
		Handler: h2c.NewHandler(mainHandler, h2Server),
	}

	logger.Infof("🚀 [XHTTP 双模网关] 监听端口: %s (自适应 H2C 与 HTTP/1.1 POST)", addr)

	go func() {
		if err := g.server.Serve(l); err != nil && err != http.ErrServerClosed {
			logger.Errorf("[XHTTP 双模网关] 服务异常: %v", err)
		}
	}()

	return nil
}

func (g *XHTTPGateway) Close() error {
	g.transportsMu.Lock()
	for _, entry := range g.transports {
		entry.transport.CloseIdleConnections()
	}
	g.transports = make(map[string]*transportEntry)
	g.transportsMu.Unlock()

	if g.server != nil {
		return g.server.Close()
	}
	return nil
}

func (g *XHTTPGateway) getOrCreateTransport(connectHost string, connectPort int, backendSni string) *http2.Transport {
	key := fmt.Sprintf("%s:%d#%s", connectHost, connectPort, backendSni)

	g.transportsMu.Lock()
	defer g.transportsMu.Unlock()

	now := time.Now()
	if entry, exists := g.transports[key]; exists {
		entry.lastUsed = now
		return entry.transport
	}

	// LRU 淘汰：若超过 64 个，淘汰最久未使用的 Transport 并关闭连接
	const maxTransports = 64
	if len(g.transports) >= maxTransports {
		var oldestKey string
		var oldestTime time.Time
		first := true
		for k, v := range g.transports {
			if first || v.lastUsed.Before(oldestTime) {
				oldestKey = k
				oldestTime = v.lastUsed
				first = false
			}
		}
		if oldestKey != "" {
			if oldEntry, ok := g.transports[oldestKey]; ok {
				oldEntry.transport.CloseIdleConnections()
				delete(g.transports, oldestKey)
			}
		}
	}

	targetAddr := net.JoinHostPort(connectHost, strconv.Itoa(connectPort))
	t := &http2.Transport{
		TLSClientConfig: &tls.Config{
			ServerName:         backendSni,
			InsecureSkipVerify: true,
			NextProtos:         []string{"h2"},
		},
		DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
			rawConn, err := dialer.DialContextWithTimeout(ctx, "tcp", targetAddr, 7*time.Second)
			if err != nil {
				return nil, err
			}
			tlsConn := tls.Client(rawConn, cfg)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				rawConn.Close()
				return nil, err
			}
			return tlsConn, nil
		},
		AllowHTTP:       false,
		ReadIdleTimeout: 30 * time.Second,
		PingTimeout:     10 * time.Second,
	}

	g.transports[key] = &transportEntry{
		transport: t,
		lastUsed:  now,
	}
	return t
}

func (g *XHTTPGateway) handleWorkerForward(w http.ResponseWriter, r *http.Request, protoLabel string) {
	cfg := g.cfgMgr.Get()
	rawPath := r.RequestURI
	targetServer, cleanedPath := prefer.ParsePreferAndCleanPath(rawPath, cfg.DefaultPrefer)
	connectHost, connectPort := prefer.ParseTargetHostPort(targetServer)

	clientTag := r.RemoteAddr
	g.preferMgr.RecordRequest(targetServer, clientTag)

	// 非阻塞读取客户端握手首包（VLESS / XHTTP 握手数据，通常 200~500 字节，严禁 LimitReader 堵塞）
	firstBufPtr := GetBuffer32k()
	firstBuf := *firstBufPtr
	n, readErr := r.Body.Read(firstBuf)
	if readErr != nil && readErr != io.EOF && n == 0 {
		PutBuffer32k(firstBufPtr)
		return
	}
	handshakeBytes := make([]byte, n)
	copy(handshakeBytes, firstBuf[:n])
	PutBuffer32k(firstBufPtr)

	var triedBackends []string
	const maxRetries = 2

	for attempt := 0; attempt <= maxRetries; attempt++ {
		cachedAccounts := g.quotaMgr.GetCachedAccounts()
		backendSni := g.healthMgr.GetNextWorkerBackend(triedBackends, cachedAccounts)
		if backendSni == "" {
			http.Error(w, "无可用 Worker 后端节点", http.StatusServiceUnavailable)
			return
		}
		triedBackends = append(triedBackends, backendSni)

		if attempt == 0 {
			logger.Infof("------------------------------------------------------------\n"+
				"[%s #%s] 收到流式请求:\n"+
				"  📥 [客户端入站 Ingress]\n"+
				"     • 客户端来源: %s\n"+
				"     • 客户端 Host: %s\n"+
				"     • 请求方式 Method: %s\n"+
				"     • 客户端原始请求路径: %s\n"+
				"  📤 [网关转发出站 Egress]\n"+
				"     • 实际 Server: %s:%d\n"+
				"     • 实际 TLS SNI: %s (Worker后端)\n"+
				"     • 实际 HTTP Host: %s\n"+
				"     • 实际请求路径: %s\n"+
				"------------------------------------------------------------",
				protoLabel, clientTag, clientTag, r.Host, r.Method, rawPath, connectHost, connectPort, backendSni, backendSni, cleanedPath)
		} else {
			logger.Warnf("[%s Failover #%s] 🔄 正在透明故障转移至备用 Worker 后端 (第 %d 次重试): %s", protoLabel, clientTag, attempt, backendSni)
		}

		startTime := time.Now()
		outURL := fmt.Sprintf("https://%s%s", backendSni, cleanedPath)

		// 建立全双工实时上行数据管道
		pr, pw := io.Pipe()

		handshakePassed := make(chan struct{})
		abortRetry := make(chan struct{})

		go func(writer *io.PipeWriter) {
			countingDest := &traffic.CountingWriter{
				Writer:  writer,
				OnWrite: traffic.GlobalTracker.RecordTx,
			}
			// 1. 先仅发送暂存的握手首包
			if len(handshakeBytes) > 0 {
				if _, wErr := countingDest.Write(handshakeBytes); wErr != nil {
					_ = writer.CloseWithError(wErr)
					return
				}
			}

			// 2. 关键防御：在远端握手成功前绝不读取 r.Body，避免重试时后续数据已被消费截断
			select {
			case <-handshakePassed:
				// 握手通过，无缝接管客户端后续持续灌入的上行数据（复用 32KB 内存池，零堆分配）
				upBufPtr := GetBuffer32k()
				_, copyErr := io.CopyBuffer(countingDest, r.Body, *upBufPtr)
				PutBuffer32k(upBufPtr)
				if copyErr != nil {
					_ = writer.CloseWithError(copyErr)
				} else {
					_ = writer.Close()
				}
			case <-abortRetry:
				// 握手被阻断，终止当前管道并保留完整的 r.Body 供下一轮重试
				_ = writer.Close()
			}
		}(pw)

		outReq, err := http.NewRequestWithContext(r.Context(), r.Method, outURL, pr)
		if err != nil {
			close(abortRetry)
			_ = pw.Close()
			continue
		}

		// 过滤跳跃式头部，安全透传业务头部
		for k, vv := range r.Header {
			lower := strings.ToLower(k)
			if lower != "host" && lower != "connection" && lower != "keep-alive" && lower != "transfer-encoding" && lower != "upgrade" {
				if lower == "te" && r.Header.Get("te") != "trailers" {
					continue
				}
				for _, v := range vv {
					outReq.Header.Add(k, v)
				}
			}
		}
		outReq.Host = backendSni

		transport := g.getOrCreateTransport(connectHost, connectPort, backendSni)
		resp, err := transport.RoundTrip(outReq)
		if err != nil {
			close(abortRetry)
			_ = pw.Close()
			logger.Warnf("[%s 优选链路传输异常 #%s -> %s]: %v (优选服务器不可达，不轮换后端)", protoLabel, clientTag, targetServer, err)
			g.preferMgr.RecordResponse(targetServer, 0, "error", err.Error())
			// 优选服务器底层异常，严禁归咎于 Worker 后端或在同一失效优选上无谓轮换后端
			return
		}

		// 核心准则：仅在已连接上优选服务器后，收到后端明确返回阻断状态码 (1101, 502, 429, 521, 503) 时，才触发后端熔断与轮换
		if resp.StatusCode == 1101 || resp.StatusCode == 502 || resp.StatusCode == 429 || resp.StatusCode == 521 || resp.StatusCode == 503 {
			_ = resp.Body.Close()
			close(abortRetry) // 通知当前管道安全退出，不消费客户端后续 r.Body
			_ = pw.Close()
			g.healthMgr.MarkBackendFailure(backendSni, fmt.Sprintf("HTTP %d", resp.StatusCode))
			logger.Warnf("[%s 收到后端阻断状态码/触发熔断与轮换 #%s -> %s]: HTTP %d", protoLabel, clientTag, backendSni, resp.StatusCode)
			continue
		}

		// 握手成功！解除阻塞，开始持续流式转发客户端后续数据
		close(handshakePassed)

		elapsed := int(time.Since(startTime).Milliseconds())
		g.healthMgr.MarkBackendSuccess(backendSni)
		g.preferMgr.RecordResponse(targetServer, elapsed, "ok", fmt.Sprintf("HTTP %d", resp.StatusCode))
		logger.Infof("[%s #%s] ✅ 握手成功: 状态码 %d (耗时: %dms) | 实际 Server: %s:%d | SNI: %s",
			protoLabel, clientTag, resp.StatusCode, elapsed, connectHost, connectPort, backendSni)

		// 回传远端响应头至客户端
		for k, vv := range resp.Header {
			lower := strings.ToLower(k)
			if lower != "connection" && lower != "keep-alive" && lower != "transfer-encoding" {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
		}

		w.WriteHeader(resp.StatusCode)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}

		// 下行数据流式实时灌入（带 Flush，确保代理握手首包与后续流零延迟抵达客户端）
		flusher, isFlusher := w.(http.Flusher)
		downBufPtr := GetBuffer32k()
		downBuf := *downBufPtr
		for {
			nr, er := resp.Body.Read(downBuf)
			if nr > 0 {
				traffic.GlobalTracker.RecordRx(int64(nr))
				if _, wErr := w.Write(downBuf[:nr]); wErr != nil {
					// 客户端已提前断开连接，立即退出并关闭远端流，避免无效流量消耗
					break
				}
				if isFlusher {
					flusher.Flush()
				}
			}
			if er != nil {
				break
			}
		}
		PutBuffer32k(downBufPtr)
		_ = resp.Body.Close()
		return
	}
}
