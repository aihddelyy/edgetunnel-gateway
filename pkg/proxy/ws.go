package proxy

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"edgetunnel-lb/pkg/config"
	"edgetunnel-lb/pkg/health"
	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/model"
	"edgetunnel-lb/pkg/prefer"
	"edgetunnel-lb/pkg/quota"
)

type WSGateway struct {
	cfgMgr    *config.Manager
	healthMgr *health.Manager
	preferMgr *prefer.Manager
	quotaMgr  *quota.Manager
	metrics   *model.Metrics
	listener  net.Listener
	server    *http.Server
}

func NewWSGateway(cfgMgr *config.Manager, healthMgr *health.Manager, preferMgr *prefer.Manager, quotaMgr *quota.Manager, metrics *model.Metrics) *WSGateway {
	return &WSGateway{
		cfgMgr:    cfgMgr,
		healthMgr: healthMgr,
		preferMgr: preferMgr,
		quotaMgr:  quotaMgr,
		metrics:   metrics,
	}
}

func (g *WSGateway) Start() error {
	cfg := g.cfgMgr.Get()
	addr := fmt.Sprintf(":%d", cfg.HTTPPort)

	mux := http.NewServeMux()
	mux.HandleFunc("/", g.handleWS)

	g.server = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	l, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	g.listener = l

	logger.Infof("🚀 [WebSocket 网关] 监听端口: %s (Pages 专属分流)", addr)
	go func() {
		if err := g.server.Serve(l); err != nil && err != http.ErrServerClosed {
			logger.Errorf("[WebSocket 网关] 服务异常: %v", err)
		}
	}()
	return nil
}

func (g *WSGateway) Close() error {
	if g.server != nil {
		return g.server.Close()
	}
	return nil
}

func (g *WSGateway) handleWS(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("EdgeTunnel WS Gateway is running.\n"))
		return
	}

	if g.metrics != nil {
		atomic.AddInt64(&g.metrics.WSRequests, 1)
	}

	cfg := g.cfgMgr.Get()
	rawPath := r.RequestURI
	targetServer, cleanedPath := prefer.ParsePreferAndCleanPath(rawPath, cfg.DefaultPrefer)
	connectHost, connectPort := prefer.ParseTargetHostPort(targetServer)

	clientTag := r.RemoteAddr
	g.preferMgr.RecordActivity(targetServer, 0, "ok", "WS连接握手")

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	if tcpConn, ok := clientConn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
		_ = tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetKeepAlivePeriod(15 * time.Second)
	}

	// Prepare outbound handshake headers
	outHeaders := make(http.Header)
	for k, vv := range r.Header {
		lower := strings.ToLower(k)
		if lower != "host" && lower != "upgrade" && lower != "connection" {
			for _, v := range vv {
				outHeaders.Add(k, v)
			}
		}
	}
	outHeaders.Set("Upgrade", "websocket")
	outHeaders.Set("Connection", "Upgrade")

	var triedBackends []string
	const maxRetries = 2

	for attempt := 0; attempt <= maxRetries; attempt++ {
		cachedAccounts := g.quotaMgr.GetCachedAccounts()
		backendSni := g.healthMgr.GetNextPagesBackend(triedBackends, cachedAccounts)
		if backendSni == "" {
			logger.Warnf("[WS Gateway] 无可用 Pages 后端节点")
			return
		}
		triedBackends = append(triedBackends, backendSni)

		if attempt == 0 {
			logger.Infof("------------------------------------------------------------\n"+
				"[WS/Upgrade #%s] 收到握手请求:\n"+
				"  📥 [客户端入站 Ingress]\n"+
				"     • 客户端来源: %s\n"+
				"     • 客户端 Host: %s\n"+
				"     • 客户端原始请求路径: %s\n"+
				"  📤 [网关转发出站 Egress]\n"+
				"     • 实际 Server: %s:%d\n"+
				"     • 实际 TLS SNI: %s (Pages后端)\n"+
				"     • 实际 HTTP Host: %s\n"+
				"     • 实际请求路径: %s\n"+
				"------------------------------------------------------------",
				clientTag, clientTag, r.Host, rawPath, connectHost, connectPort, backendSni, backendSni, cleanedPath)
		} else {
			logger.Warnf("[WS Failover #%s] 🔄 正在透明故障转移至备用 Pages 后端 (第 %d 次重试): %s", clientTag, attempt, backendSni)
		}

		startTime := time.Now()
		tlsConfig := &tls.Config{
			ServerName:         backendSni,
			InsecureSkipVerify: true,
			NextProtos:         []string{"http/1.1"},
		}

		targetAddr := net.JoinHostPort(connectHost, strconv.Itoa(connectPort))
		dialer := &net.Dialer{Timeout: 7 * time.Second}
		rawRemote, err := dialer.Dial("tcp", targetAddr)
		if err != nil {
			logger.Warnf("[WS 优选链路连接失败 #%s -> %s]: %v", clientTag, targetAddr, err)
			g.preferMgr.RecordActivity(targetServer, 0, "error", err.Error())
			continue
		}

		remoteConn := tls.Client(rawRemote, tlsConfig)
		_ = remoteConn.SetDeadline(time.Now().Add(7 * time.Second))
		if err := remoteConn.Handshake(); err != nil {
			remoteConn.Close()
			logger.Warnf("[WS TLS 握手失败 #%s -> %s]: %v", clientTag, backendSni, err)
			g.healthMgr.MarkBackendFailure(backendSni, err.Error())
			continue
		}

		// Construct HTTP GET request
		reqLine := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\n", cleanedPath, backendSni)
		reqBuf := bytesBufferPool.Get()
		reqBuf.WriteString(reqLine)
		_ = outHeaders.Write(reqBuf)
		reqBuf.WriteString("\r\n")

		if _, err := remoteConn.Write(reqBuf.Bytes()); err != nil {
			bytesBufferPool.Put(reqBuf)
			remoteConn.Close()
			continue
		}
		bytesBufferPool.Put(reqBuf)

		// Read response
		remoteReader := bufio.NewReader(remoteConn)
		resp, err := http.ReadResponse(remoteReader, r)
		if err != nil {
			remoteConn.Close()
			g.healthMgr.MarkBackendFailure(backendSni, err.Error())
			continue
		}

		if resp.StatusCode != http.StatusSwitchingProtocols {
			resp.Body.Close()
			remoteConn.Close()
			g.healthMgr.MarkBackendFailure(backendSni, fmt.Sprintf("HTTP %d", resp.StatusCode))
			logger.Warnf("[WS 远端握手未通过 #%s -> %s]: 返回状态码 %d", clientTag, backendSni, resp.StatusCode)
			continue
		}

		// Handshake successful!
		_ = remoteConn.SetDeadline(time.Time{})
		elapsed := int(time.Since(startTime).Milliseconds())
		g.healthMgr.MarkBackendSuccess(backendSni)
		g.preferMgr.RecordActivity(targetServer, elapsed, "ok", "WS握手成功")
		logger.Infof("[WS/Upgrade #%s] ✅ 握手成功: 101 Switching Protocols (耗时: %dms) | 实际 Server: %s:%d | SNI: %s",
			clientTag, elapsed, connectHost, connectPort, backendSni)

		// Write 101 Switching Protocols to client
		if err := resp.Write(clientConn); err != nil {
			remoteConn.Close()
			return
		}

		// Bi-directional pipe
		pipeConn(clientConn, clientBuf.Reader, remoteConn, remoteReader)
		return
	}
}

func pipeConn(c1 net.Conn, r1 io.Reader, c2 net.Conn, r2 io.Reader) {
	errCh := make(chan error, 2)

	go func() {
		bufPtr := GetBuffer32k()
		defer PutBuffer32k(bufPtr)

		var src io.Reader = c1
		if r1 != nil {
			src = io.MultiReader(r1, c1)
		}
		_, err := io.CopyBuffer(c2, src, *bufPtr)
		errCh <- err
	}()

	go func() {
		bufPtr := GetBuffer32k()
		defer PutBuffer32k(bufPtr)

		var src io.Reader = c2
		if r2 != nil {
			src = io.MultiReader(r2, c2)
		}
		_, err := io.CopyBuffer(c1, src, *bufPtr)
		errCh <- err
	}()

	<-errCh
	_ = c1.Close()
	_ = c2.Close()
}
