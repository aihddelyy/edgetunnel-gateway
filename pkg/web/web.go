package web

import (
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httputil"
	"net/url"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"edgetunnel-lb/pkg/config"
	"edgetunnel-lb/pkg/health"
	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/model"
	"edgetunnel-lb/pkg/notify"
	"edgetunnel-lb/pkg/prefer"
	"edgetunnel-lb/pkg/quota"
	"edgetunnel-lb/pkg/updater"
	"edgetunnel-lb/pkg/version"
)

//go:embed public/*
var embeddedPublic embed.FS

type SessionInfo struct {
	Username  string
	ExpiresAt int64
}

type Server struct {
	cfgMgr    *config.Manager
	healthMgr *health.Manager
	preferMgr *prefer.Manager
	quotaMgr  *quota.Manager
	metrics   *model.Metrics

	httpServer *http.Server

	sessionsMu sync.RWMutex
	sessions   map[string]SessionInfo

	trafficHistoryMu sync.RWMutex
	trafficHistory   []model.TrafficPoint
	lastSampleXhttp  int64
	lastSampleWs     int64
}

func NewWebServer(cfgMgr *config.Manager, healthMgr *health.Manager, preferMgr *prefer.Manager, quotaMgr *quota.Manager, metrics *model.Metrics) *Server {
	s := &Server{
		cfgMgr:         cfgMgr,
		healthMgr:      healthMgr,
		preferMgr:      preferMgr,
		quotaMgr:       quotaMgr,
		metrics:        metrics,
		sessions:       make(map[string]SessionInfo),
		trafficHistory: make([]model.TrafficPoint, 0, 24),
	}
	s.initTrafficBaseline()
	s.startTrafficSampler()
	s.startMemoryGuard()
	return s
}

func (s *Server) Start() error {
	cfg := s.cfgMgr.Get()
	addr := fmt.Sprintf(":%d", cfg.WebPort)

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	logger.Infof("🖥️ [Web 控制台与 API] 监听端口: http://0.0.0.0:%d", cfg.WebPort)
	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("[Web 控制台] 服务异常: %v", err)
		}
	}()
	return nil
}

func (s *Server) Close() error {
	if s.httpServer != nil {
		return s.httpServer.Close()
	}
	return nil
}

// ----------------- Auth Helpers -----------------

func hashPassword(password, salt string) string {
	hasher := sha256.New()
	hasher.Write([]byte(password + ":" + salt))
	return hex.EncodeToString(hasher.Sum(nil))
}

func (s *Server) checkAuth(r *http.Request) bool {
	cfg := s.cfgMgr.Get()
	if !cfg.Auth.Enabled || cfg.Auth.PasswordHash == "" {
		return true
	}

	token := ""
	authHeader := r.Header.Get("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token = strings.TrimSpace(authHeader[7:])
	}
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		return false
	}

	s.sessionsMu.RLock()
	sess, exists := s.sessions[token]
	s.sessionsMu.RUnlock()

	if !exists {
		return false
	}
	if time.Now().UnixMilli() > sess.ExpiresAt {
		s.sessionsMu.Lock()
		delete(s.sessions, token)
		s.sessionsMu.Unlock()
		return false
	}
	return true
}

func (s *Server) createSession(username string, days int) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)

	s.sessionsMu.Lock()
	s.sessions[token] = SessionInfo{
		Username:  username,
		ExpiresAt: time.Now().Add(time.Duration(days) * 24 * time.Hour).UnixMilli(),
	}
	s.sessionsMu.Unlock()
	return token
}

// ----------------- Traffic History -----------------

func (s *Server) initTrafficBaseline() {
	now := time.Now()
	s.trafficHistoryMu.Lock()
	defer s.trafficHistoryMu.Unlock()

	for i := 11; i >= 0; i-- {
		t := now.Add(-time.Duration(i*5) * time.Minute)
		s.trafficHistory = append(s.trafficHistory, model.TrafficPoint{
			Time: t.Format("15:04"),
		})
	}
}

func (s *Server) startTrafficSampler() {
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			s.recordTrafficSample()
		}
	}()
}

func (s *Server) startMemoryGuard() {
	go func() {
		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			cfg := s.cfgMgr.Get()
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)

			sysMB := mem.Sys / 1024 / 1024
			heapIdleMB := mem.HeapIdle / 1024 / 1024

			// 1. 如果用户启用了配置阈值并超过该上限，强制全面 GC 并退还内存给 OS
			if cfg.MemoryGuard.Enabled && cfg.MemoryGuard.ThresholdMB > 0 {
				if sysMB >= uint64(cfg.MemoryGuard.ThresholdMB) {
					logger.Warnf("[MemoryGuard] 🚨 系统内存占用 (%dMB) 超过阈值 (%dMB)，正在主动回收并归还物理内存...", sysMB, cfg.MemoryGuard.ThresholdMB)
					runtime.GC()
					debug.FreeOSMemory()
					continue
				}
			}

			// 2. 嵌入式/OpenWrt 智能常驻优化：只要有超过 4MB 未释放的空闲堆碎片，主动归还操作系统
			if heapIdleMB >= 4 {
				runtime.GC()
				debug.FreeOSMemory()
			}
		}
	}()
}

func (s *Server) recordTrafficSample() {
	curX := atomic.LoadInt64(&s.metrics.XHTTPRequests)
	curW := atomic.LoadInt64(&s.metrics.WSRequests)
	curH2C := atomic.LoadInt64(&s.metrics.H2CRequests)
	curH1 := atomic.LoadInt64(&s.metrics.H1Requests)

	s.trafficHistoryMu.Lock()
	defer s.trafficHistoryMu.Unlock()

	pt := model.TrafficPoint{
		Time:     time.Now().Format("15:04"),
		WS:       curW - s.lastSampleWs,
		XHTTP:    curX - s.lastSampleXhttp,
		H2C:      curH2C,
		H1:       curH1,
		TotalReq: curW + curX,
	}
	if pt.WS < 0 {
		pt.WS = 0
	}
	if pt.XHTTP < 0 {
		pt.XHTTP = 0
	}

	s.lastSampleXhttp = curX
	s.lastSampleWs = curW

	s.trafficHistory = append(s.trafficHistory, pt)
	if len(s.trafficHistory) > 24 {
		s.trafficHistory = s.trafficHistory[1:]
	}
}

// ----------------- Router Registration -----------------

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// 1. Static Files (SPA)
	subFS, err := fs.Sub(embeddedPublic, "public")
	var staticHandler http.Handler
	if err == nil {
		staticHandler = http.FileServer(http.FS(subFS))
	} else {
		staticHandler = http.FileServer(http.Dir("public"))
	}

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/api/") {
			s.handleAPI(w, r)
			return
		}
		// Serve embedded files
		staticHandler.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Public APIs
	if path == "/api/auth/status" && r.Method == http.MethodGet {
		cfg := s.cfgMgr.Get()
		hasPassword := cfg.Auth.PasswordHash != ""
		isAuth := s.checkAuth(r)
		user := cfg.Auth.Username
		if user == "" {
			user = "admin"
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"needSetup":     !hasPassword,
			"authenticated": isAuth,
			"username":      user,
		})
		return
	}

	if path == "/api/auth/setup" && r.Method == http.MethodPost {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Password) < 4 {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "密码长度至少需 4 位"})
			return
		}

		saltBytes := make([]byte, 16)
		_, _ = rand.Read(saltBytes)
		salt := hex.EncodeToString(saltBytes)

		user := strings.TrimSpace(req.Username)
		if user == "" {
			user = "admin"
		}
		pwdHash := hashPassword(req.Password, salt)

		_ = s.cfgMgr.Update(func(cfg *model.Config) {
			cfg.Auth.Enabled = true
			cfg.Auth.Username = user
			cfg.Auth.PasswordHash = pwdHash
			cfg.Auth.Salt = salt
		})

		token := s.createSession(user, 7)
		logger.Infof("[Web Auth] 管理员账号首次初始化成功: %s", user)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "token": token, "username": user})
		return
	}

	if path == "/api/auth/login" && r.Method == http.MethodPost {
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "无效请求参数"})
			return
		}

		cfg := s.cfgMgr.Get()
		if cfg.Auth.PasswordHash == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "系统尚未初始化密码"})
			return
		}
		if req.Username != cfg.Auth.Username || hashPassword(req.Password, cfg.Auth.Salt) != cfg.Auth.PasswordHash {
			writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "用户名或密码错误"})
			return
		}

		token := s.createSession(cfg.Auth.Username, 7)
		logger.Infof("[Web Auth] 管理员成功登录: %s", cfg.Auth.Username)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "token": token, "username": cfg.Auth.Username})
		return
	}

	if path == "/api/auth/logout" && r.Method == http.MethodPost {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Bearer ") {
			s.sessionsMu.Lock()
			delete(s.sessions, strings.TrimSpace(authHeader[7:]))
			s.sessionsMu.Unlock()
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
		return
	}

	// Auth Guard for other administrative routes
	isPublic := path == "/api/status" || path == "/api/prefer/ping" || path == "/api/ping-all" || path == "/api/memory/status" ||
		path == "/api/update/check" ||
		(strings.HasPrefix(path, "/api/cf-panel/") && path != "/api/cf-panel/target")
	if !isPublic && !s.checkAuth(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{"ok": false, "error": "请先登录以执行管理员操作"})
		return
	}

	// ----------------- Core Dashboard APIs -----------------

	if path == "/api/status" && r.Method == http.MethodGet {
		cfg := s.cfgMgr.Get()
		accounts := s.quotaMgr.GetCachedAccounts()
		healthMap := s.healthMgr.GetHealthSnapshot()
		statsMap := s.healthMgr.GetStatsSnapshot()
		nodes := s.preferMgr.GetNodesList()

		s.trafficHistoryMu.RLock()
		tfHistory := make([]model.TrafficPoint, len(s.trafficHistory))
		copy(tfHistory, s.trafficHistory)
		s.trafficHistoryMu.RUnlock()

		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		sysMB := memStats.Sys / 1024 / 1024
		allocMB := memStats.Alloc / 1024 / 1024

		respData := map[string]interface{}{
			"version": version.CurrentVersion,
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
			"uptime":  time.Now().Unix() - s.metrics.StartTime,
			"cluster": map[string]interface{}{
				"enabled":     true,
				"workerCount": runtime.NumCPU(),
				"cpuCount":    runtime.NumCPU(),
			},
			"memoryMB": allocMB,
			"memoryDetail": map[string]interface{}{
				"rssMB":       sysMB,
				"heapUsedMB":  allocMB,
				"heapTotalMB": memStats.HeapSys / 1024 / 1024,
			},
			"metrics":          s.metrics,
			"health":           healthMap,
			"preferNodes":      nodes,
			"trafficHistory":   tfHistory,
			"backendStats":     statsMap,
			"disabledBackends": cfg.DisabledBackends,
			"config": map[string]interface{}{
				"workerBackends":         cfg.WorkerBackends,
				"pagesBackends":          cfg.PagesBackends,
				"defaultPrefer":          cfg.DefaultPrefer,
				"backendAccountMap":      cfg.BackendAccountMap,
				"quotaDisplayMode":       cfg.QuotaDisplayMode,
				"disabledBackends":       cfg.DisabledBackends,
				"cfQuotaAvoidThreshold":  cfg.CFQuotaAvoidThreshold,
				"memoryGuard":            cfg.MemoryGuard,
			},
			"cfAccountsBrief":        accounts,
			"cfQuotaLastUpdated":     s.quotaMgr.GetLastUpdatedStr(),
			"cfQuotaIntervalMinutes": cfg.CFQuotaIntervalMinutes,
		}
		writeJSON(w, http.StatusOK, respData)
		return
	}

	if path == "/api/config" {
		if r.Method == http.MethodGet {
			cfg := s.cfgMgr.Get()
			// hide sensitive salt/hash
			cfg.Auth.PasswordHash = ""
			cfg.Auth.Salt = ""
			writeJSON(w, http.StatusOK, cfg)
			return
		}
		if r.Method == http.MethodPost {
			var newCfg model.Config
			if err := json.NewDecoder(r.Body).Decode(&newCfg); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
				return
			}
			err := s.cfgMgr.Update(func(c *model.Config) {
				if newCfg.DefaultPrefer != "" {
					c.DefaultPrefer = newCfg.DefaultPrefer
				}
				if len(newCfg.WorkerBackends) > 0 {
					c.WorkerBackends = newCfg.WorkerBackends
				}
				if len(newCfg.PagesBackends) > 0 {
					c.PagesBackends = newCfg.PagesBackends
				}
				if newCfg.CFQuotaAvoidThreshold > 0 {
					c.CFQuotaAvoidThreshold = newCfg.CFQuotaAvoidThreshold
				}
				if newCfg.CFAlertThreshold > 0 {
					c.CFAlertThreshold = newCfg.CFAlertThreshold
				}
				if newCfg.BackendAccountMap != nil {
					c.BackendAccountMap = newCfg.BackendAccountMap
				}
				if newCfg.QuotaDisplayMode != "" {
					c.QuotaDisplayMode = newCfg.QuotaDisplayMode
				}
				if newCfg.Telegram.BotToken != "" || newCfg.Telegram.ChatID != "" {
					c.Telegram = newCfg.Telegram
					c.Telegram.ChatID = config.CleanTelegramChatID(c.Telegram.ChatID)
				}
			})
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
				return
			}
			s.quotaMgr.StartPatrol()
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "saved": true})
			return
		}
	}

	if path == "/api/config/backups" && r.Method == http.MethodGet {
		backups := s.cfgMgr.GetBackupsList()
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "backups": backups})
		return
	}

	if path == "/api/config/rollback" && r.Method == http.MethodPost {
		var req struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "缺少快照 ID"})
			return
		}
		restored, err := s.cfgMgr.Rollback(req.ID)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		s.quotaMgr.StartPatrol()
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "config": restored})
		return
	}

	if path == "/api/backend/toggle" && r.Method == http.MethodPost {
		var req struct {
			Backend  string `json:"backend"`
			Disabled bool   `json:"disabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Backend == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "缺少 backend 参数"})
			return
		}

		backend := strings.TrimSpace(req.Backend)
		_ = s.cfgMgr.Update(func(cfg *model.Config) {
			list := cfg.DisabledBackends
			if req.Disabled {
				found := false
				for _, b := range list {
					if b == backend {
						found = true
						break
					}
				}
				if !found {
					cfg.DisabledBackends = append(list, backend)
				}
			} else {
				newList := make([]string, 0, len(list))
				for _, b := range list {
					if b != backend {
						newList = append(newList, b)
					}
				}
				cfg.DisabledBackends = newList
			}
		})

		cfg := s.cfgMgr.Get()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":               true,
			"backend":          backend,
			"disabled":         req.Disabled,
			"disabledBackends": cfg.DisabledBackends,
		})
		return
	}

	// Prefer Nodes API
	if path == "/api/prefer/nodes" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "nodes": s.preferMgr.GetNodesList()})
		return
	}

	if (path == "/api/prefer/ping" || path == "/api/ping-all") && (r.Method == http.MethodGet || r.Method == http.MethodPost) {
		tQuery := r.URL.Query().Get("target")
		var targets []string
		if tQuery != "" {
			targets = []string{tQuery}
		} else {
			for _, n := range s.preferMgr.GetNodesList() {
				targets = append(targets, n.Target)
			}
			if len(targets) == 0 {
				cfg := s.cfgMgr.Get()
				if cfg.DefaultPrefer != "" {
					targets = []string{cfg.DefaultPrefer}
				}
			}
		}

		var wg sync.WaitGroup
		results := make([]prefer.PingResult, len(targets))
		for i, t := range targets {
			wg.Add(1)
			go func(idx int, target string) {
				defer wg.Done()
				res := prefer.PingTarget(target, 3500*time.Millisecond)
				results[idx] = res
				s.preferMgr.RecordActivity(target, res.Latency, res.Status, "测速Ping")
			}(i, t)
		}
		wg.Wait()

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":      true,
			"count":   len(results),
			"results": results,
			"nodes":   s.preferMgr.GetNodesList(),
		})
		return
	}

	if path == "/api/prefer/set-default" && r.Method == http.MethodPost {
		var req struct {
			Target string `json:"target"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Target == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "目标不能为空"})
			return
		}
		target := strings.TrimSpace(req.Target)
		_ = s.cfgMgr.Update(func(cfg *model.Config) {
			cfg.DefaultPrefer = target
		})
		s.preferMgr.RecordActivity(target, 0, "ok", "设为默认优选", true)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "defaultPrefer": target, "nodes": s.preferMgr.GetNodesList()})
		return
	}

	if path == "/api/prefer/clear" && r.Method == http.MethodPost {
		s.preferMgr.ClearNonDefault()
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "nodes": s.preferMgr.GetNodesList()})
		return
	}

	if path == "/api/circuit/reset" && r.Method == http.MethodPost {
		s.healthMgr.ResetAllCircuit()
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "message": "所有后端熔断状态已重置"})
		return
	}

	// CF Quota & Panel API
	if path == "/api/cf-panel/accounts" && r.Method == http.MethodGet {
		force := r.URL.Query().Get("force") == "1"
		res := s.quotaMgr.FetchAccounts(force)
		writeJSON(w, http.StatusOK, res)
		return
	}

	if path == "/api/cf-panel/target" {
		if r.Method == http.MethodGet {
			cfg := s.cfgMgr.Get()
			writeJSON(w, http.StatusOK, map[string]interface{}{"target": cfg.CFPanelURL})
			return
		}
		if r.Method == http.MethodPost {
			var req struct {
				Target string `json:"target"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Target == "" {
				writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": "请提供有效的目标地址"})
				return
			}
			_ = s.cfgMgr.Update(func(cfg *model.Config) {
				cfg.CFPanelURL = strings.TrimSpace(req.Target)
			})
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "target": req.Target})
			return
		}
	}

	if path == "/api/cf-config" && r.Method == http.MethodPost {
		var req struct {
			Mode                   string                  `json:"mode"`
			AlertThreshold         int                     `json:"alertThreshold"`
			CFQuotaIntervalMinutes int                     `json:"cfQuotaIntervalMinutes"`
			URL                    string                  `json:"url"`
			Username               string                  `json:"username"`
			Password               string                  `json:"password"`
			Direct                 *model.CFDirectConfig   `json:"direct"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}

		_ = s.cfgMgr.Update(func(cfg *model.Config) {
			if req.Mode != "" {
				cfg.CFQuotaMode = req.Mode
			}
			if req.AlertThreshold > 0 {
				cfg.CFAlertThreshold = req.AlertThreshold
			}
			if req.CFQuotaIntervalMinutes > 0 {
				cfg.CFQuotaIntervalMinutes = req.CFQuotaIntervalMinutes
			}

			if req.Mode == "direct" && req.Direct != nil {
				cfg.CFDirect = *req.Direct
			} else if req.Mode != "direct" {
				if req.URL != "" {
					cfg.CFPanel.URL = req.URL
					cfg.CFPanelURL = req.URL
				}
				if req.Username != "" {
					cfg.CFPanel.Username = req.Username
				}
				if req.Password != "" {
					cfg.CFPanel.Password = req.Password
				}
			}
		})

		s.quotaMgr.StartPatrol()
		testRes := s.quotaMgr.FetchAccounts(true)
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "message": "CF 配额凭据保存成功！", "data": testRes})
		return
	}

	// CF Panel Credentials 保存与验证接口
	if path == "/api/cf-panel/credentials" && r.Method == http.MethodPost {
		var payload struct {
			URL            string `json:"url"`
			Username       string `json:"username"`
			Password       string `json:"password"`
			AlertThreshold int    `json:"alertThreshold"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		if payload.Username == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{"ok": false, "error": "请提供面板管理员账号与密码"})
			return
		}

		_ = s.cfgMgr.Update(func(cfg *model.Config) {
			cfg.CFQuotaMode = "usagepanel"
			if payload.URL != "" {
				cfg.CFPanel.URL = payload.URL
				cfg.CFPanelURL = payload.URL
			}
			cfg.CFPanel.Username = payload.Username
			if payload.Password != "" {
				cfg.CFPanel.Password = payload.Password
			}
			if payload.AlertThreshold > 0 {
				cfg.CFAlertThreshold = payload.AlertThreshold
			}
		})

		s.quotaMgr.StartPatrol()
		testRes := s.quotaMgr.FetchAccounts(true)
		if !testRes.OK {
			writeJSON(w, http.StatusOK, map[string]interface{}{
				"ok":    false,
				"error": "验证失败: " + testRes.Error,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":      true,
			"message": "CF 面板凭据验证成功！",
			"data":    testRes,
		})
		return
	}

	// UsagePanel Reverse Proxy
	if strings.HasPrefix(path, "/api/cf-panel/") && path != "/api/cf-panel/credentials" && path != "/api/cf-panel/accounts" && path != "/api/cf-panel/target" {
		cfg := s.cfgMgr.Get()
		targetBase := cfg.CFPanelURL
		if targetBase == "" {
			targetBase = "https://usagepanel.pages.dev"
		}
		targetURL, err := url.Parse(targetBase)
		if err != nil {
			targetURL, _ = url.Parse("https://usagepanel.pages.dev")
		}

		subPath := strings.TrimPrefix(path, "/api/cf-panel")
		if subPath == "" {
			subPath = "/"
		}

		proxy := httputil.NewSingleHostReverseProxy(targetURL)
		originalDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.Host = targetURL.Host
			req.URL.Path = subPath
			req.URL.RawQuery = r.URL.RawQuery
			req.Header.Del("Accept-Encoding")
		}
		proxy.ServeHTTP(w, r)
		return
	}

	// Live SSE Logs API
	if path == "/api/logs/stream" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		l := logger.GetLogger()
		snapshot := l.GetSnapshot()
		initData, _ := json.Marshal(map[string]interface{}{
			"type": "init",
			"logs": snapshot,
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", initData)
		flusher.Flush()

		ch := l.Subscribe()
		defer l.Unsubscribe(ch)

		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		notifyClose := r.Context().Done()
		for {
			select {
			case <-notifyClose:
				return
			case <-ticker.C:
				_, _ = fmt.Fprint(w, ": keepalive\n\n")
				flusher.Flush()
			case item, ok := <-ch:
				if !ok {
					return
				}
				itemData, _ := json.Marshal(map[string]interface{}{
					"type": "log",
					"log":  item,
				})
				_, _ = fmt.Fprintf(w, "data: %s\n\n", itemData)
				flusher.Flush()
			}
		}
	}

	// Memory & GC
	if path == "/api/memory/status" && r.Method == http.MethodGet {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		cfg := s.cfgMgr.Get()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok": true,
			"data": map[string]interface{}{
				"rssMB":       mem.Sys / 1024 / 1024,
				"heapUsedMB":  mem.Alloc / 1024 / 1024,
				"heapTotalMB": mem.HeapSys / 1024 / 1024,
			},
			"config": cfg.MemoryGuard,
		})
		return
	}

	if path == "/api/memory/gc" && r.Method == http.MethodPost {
		runtime.GC()
		debug.FreeOSMemory()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":      true,
			"message": "控制台手动一键垃圾回收执行完毕，已向操作系统内核归还空闲物理内存",
		})
		return
	}

	// Telegram Test
	if (path == "/api/telegram/test" || path == "/api/test-tg") && r.Method == http.MethodPost {
		sender := notify.GetTelegram()
		if sender == nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "Telegram 机器人未配置或未启用"})
			return
		}
		timeStr := time.Now().Format("2006-01-02 15:04:05")
		msg := fmt.Sprintf("🔔 *【EdgeTunnel 本地分流网关测试通知】*\n\n✅ 恭喜！网关与 Telegram Bot 通道测试握手成功。\n⏰ 发送时间: %s", timeStr)
		if err := sender.SendRawText(msg); err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true})
		return
	}

	// Update Check
	if path == "/api/update/check" && r.Method == http.MethodGet {
		cfg := s.cfgMgr.Get()
		res, err := updater.CheckForUpdate(cfg.Telegram.Proxy)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "data": res})
		return
	}

	// Update Apply
	if path == "/api/update/apply" && r.Method == http.MethodPost {
		var req struct {
			DownloadURL string `json:"downloadUrl"`
			UseMirror   bool   `json:"useMirror"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		cfg := s.cfgMgr.Get()
		if req.DownloadURL == "" {
			chk, err := updater.CheckForUpdate(cfg.Telegram.Proxy)
			if err != nil {
				writeJSON(w, http.StatusOK, map[string]interface{}{"ok": false, "error": "无法获取最新下载链接: " + err.Error()})
				return
			}
			if chk.DownloadURL == "" {
				writeJSON(w, http.StatusOK, map[string]interface{}{
					"ok":    false,
					"error": fmt.Sprintf("未在 Release 中找到适配当前平台 (%s/%s) 的编译包", chk.OS, chk.Arch),
				})
				return
			}
			req.DownloadURL = chk.DownloadURL
		}

		go func() {
			time.Sleep(300 * time.Millisecond)
			if err := updater.ApplyUpdate(req.DownloadURL, req.UseMirror, cfg.Telegram.Proxy); err != nil {
				logger.Errorf("[Updater] 自动下载更新失败: %v", err)
			}
		}()

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ok":      true,
			"message": "更新任务已启动，正在后台下载并原子替换，网关将在几秒后自动重启！",
		})
		return
	}

	// Restart
	if path == "/api/restart" && r.Method == http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "message": "正在安全重启网关..."})
		go func() {
			time.Sleep(300 * time.Millisecond)
			updater.RestartSelf()
		}()
		return
	}

	http.NotFound(w, r)
}
