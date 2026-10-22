package health

import (
	"math"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"edgetunnel-lb/pkg/config"
	"edgetunnel-lb/pkg/logger"
	"edgetunnel-lb/pkg/model"
	"edgetunnel-lb/pkg/notify"
)

const (
	CircuitBreakerTTL = 60 * time.Second
	FailureThreshold  = 5
)

var networkTransportRegex = regexp.MustCompile(`(?i)(ETIMEDOUT|ECONNREFUSED|EHOSTUNREACH|ENETUNREACH|ENOTFOUND|timeout|超时|ECONNRESET|EPIPE|CANCEL|stream.*destroyed|Client aborted|NGHTTP2_REFUSED_STREAM|context deadline exceeded|i/o timeout|connection reset by peer)`)

type Manager struct {
	mu            sync.RWMutex
	stats         map[string]*model.BackendStat
	health        map[string]*model.HealthState
	failureCounts map[string]int

	pagesIndex  uint64
	workerIndex uint64

	cfgMgr  *config.Manager
	metrics *model.Metrics
}

var globalManager *Manager

func InitHealthManager(cfgMgr *config.Manager, metrics *model.Metrics) *Manager {
	m := &Manager{
		stats:         make(map[string]*model.BackendStat),
		health:        make(map[string]*model.HealthState),
		failureCounts: make(map[string]int),
		cfgMgr:        cfgMgr,
		metrics:       metrics,
	}
	globalManager = m
	return m
}

func GetManager() *Manager {
	return globalManager
}

func IsNetworkTransportError(reason string) bool {
	return networkTransportRegex.MatchString(reason)
}

func (m *Manager) RecordBackendReq(backend string, isSuccess bool) {
	if backend == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	stat, exists := m.stats[backend]
	if !exists {
		stat = &model.BackendStat{}
		m.stats[backend] = stat
	}
	stat.Total++
	if isSuccess {
		stat.Success++
	} else {
		stat.Fail++
	}
}

func (m *Manager) MarkBackendSuccess(backend string) {
	if backend == "" {
		return
	}
	m.RecordBackendReq(backend, true)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.failureCounts[backend] = 0
	state, exists := m.health[backend]
	if exists && !state.IsHealthy {
		logger.Infof("[Health Check] \033[32m后端 %s 传输成功，熔断解除，静默恢复健康池！\033[0m", backend)
		delete(m.health, backend)
	}
}

func (m *Manager) MarkBackendFailure(backend, reason string) {
	if backend == "" || IsNetworkTransportError(reason) || strings.Contains(reason, "Invalid URL") {
		return
	}

	m.RecordBackendReq(backend, false)

	m.mu.Lock()
	defer m.mu.Unlock()

	m.failureCounts[backend]++
	currentFails := m.failureCounts[backend]

	existing, exists := m.health[backend]
	if exists && !existing.IsHealthy {
		return
	}

	if currentFails >= FailureThreshold {
		if m.metrics != nil {
			atomic.AddInt64(&m.metrics.CircuitTrips, 1)
		}
		logger.Warnf("[Circuit Breaker] \033[33m⚠️ 熔断告警: 后端 %s 连续遭遇 %d 次明确阻断 [%s]，自动隔离 %v\033[0m",
			backend, currentFails, reason, CircuitBreakerTTL)

		blockUntil := time.Now().Add(CircuitBreakerTTL).UnixMilli()
		m.health[backend] = &model.HealthState{
			IsHealthy:  false,
			BlockUntil: blockUntil,
			Reason:     reason,
		}

		if notifySender := notify.GetTelegram(); notifySender != nil {
			notifySender.SendCircuitAlert(backend, reason)
		}
	} else {
		logger.Warnf("[Health Check] ⚠️ 后端 %s 遇到异常 [%s] (连续失败 %d/%d 次，暂不熔断)",
			backend, reason, currentFails, FailureThreshold)
	}
}

func (m *Manager) ResetAllCircuit() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.health = make(map[string]*model.HealthState)
	m.failureCounts = make(map[string]int)
	logger.Infof("[Circuit Breaker] 所有后端熔断状态已被手动重置")
}

func (m *Manager) GetHealthyBackends(list []string, excludeBackends []string, cachedAccounts []model.CFAccountBrief) []string {
	if len(list) == 0 {
		return list
	}

	cfg := m.cfgMgr.Get()
	now := time.Now().UnixMilli()

	excludesMap := make(map[string]struct{})
	for _, e := range excludeBackends {
		excludesMap[e] = struct{}{}
	}

	disabledMap := make(map[string]struct{})
	for _, d := range cfg.DisabledBackends {
		disabledMap[d] = struct{}{}
	}

	// 1. 过滤已手动禁用的节点和本次重试排除的节点
	var enabledList []string
	for _, b := range list {
		if _, disabled := disabledMap[b]; !disabled {
			if _, excluded := excludesMap[b]; !excluded {
				enabledList = append(enabledList, b)
			}
		}
	}

	baseList := enabledList
	if len(baseList) == 0 {
		for _, b := range list {
			if _, excluded := excludesMap[b]; !excluded {
				baseList = append(baseList, b)
			}
		}
	}
	if len(baseList) == 0 {
		return list
	}

	// 2. 过滤处于熔断隔离期的节点
	m.mu.Lock()
	var unblocked []string
	for _, b := range baseList {
		s, exists := m.health[b]
		if !exists {
			unblocked = append(unblocked, b)
			continue
		}
		if !s.IsHealthy && now > s.BlockUntil {
			logger.Infof("[Circuit Breaker] ⏱️ 后端 %s 隔离期已满，重新加入轮询探活。", b)
			delete(m.health, b)
			unblocked = append(unblocked, b)
			continue
		}
		if s.IsHealthy {
			unblocked = append(unblocked, b)
		}
	}
	m.mu.Unlock()

	candidateList := unblocked
	if len(candidateList) == 0 {
		candidateList = baseList
	}

	// 3. 配额避让机制 (Quota Avoidance, 默认 95%)
	if len(cachedAccounts) > 0 && len(cfg.BackendAccountMap) > 0 {
		avoidThreshold := float64(cfg.CFQuotaAvoidThreshold)
		if avoidThreshold <= 0 || avoidThreshold > 100 {
			avoidThreshold = 95
		}

		var safeFromQuota []string
		for _, b := range candidateList {
			accID := cfg.BackendAccountMap[b]
			if accID == "" {
				safeFromQuota = append(safeFromQuota, b)
				continue
			}

			// Find account
			var matchedAcc *model.CFAccountBrief
			for _, a := range cachedAccounts {
				if a.ID == accID || a.AccountID == accID || a.Name == accID {
					matchedAcc = &a
					break
				}
			}

			if matchedAcc == nil || matchedAcc.Percent < avoidThreshold {
				safeFromQuota = append(safeFromQuota, b)
			}
		}

		if len(safeFromQuota) > 0 {
			return safeFromQuota
		}
	}

	return candidateList
}

func (m *Manager) GetNextPagesBackend(excludeBackends []string, cachedAccounts []model.CFAccountBrief) string {
	cfg := m.cfgMgr.Get()
	full := cfg.PagesBackends
	if len(full) == 0 {
		full = cfg.WorkerBackends
	}
	active := m.GetHealthyBackends(full, excludeBackends, cachedAccounts)
	if len(active) == 0 {
		return ""
	}

	idx := atomic.AddUint64(&m.pagesIndex, 1) - 1
	return active[int(idx%uint64(len(active)))]
}

func (m *Manager) GetNextWorkerBackend(excludeBackends []string, cachedAccounts []model.CFAccountBrief) string {
	cfg := m.cfgMgr.Get()
	full := cfg.WorkerBackends
	if len(full) == 0 {
		full = cfg.PagesBackends
	}
	active := m.GetHealthyBackends(full, excludeBackends, cachedAccounts)
	if len(active) == 0 {
		return ""
	}

	idx := atomic.AddUint64(&m.workerIndex, 1) - 1
	return active[int(idx%uint64(len(active)))]
}

func (m *Manager) GetStatsSnapshot() map[string]model.BackendStat {
	m.mu.RLock()
	defer m.mu.RUnlock()

	res := make(map[string]model.BackendStat, len(m.stats))
	for k, v := range m.stats {
		rate := 100.0
		if v.Total > 0 {
			rate = math.Round(float64(v.Success)/float64(v.Total)*10000.0) / 100.0
		}
		res[k] = model.BackendStat{
			Total:       v.Total,
			Success:     v.Success,
			Fail:        v.Fail,
			SuccessRate: rate,
		}
	}
	return res
}

func (m *Manager) GetHealthSnapshot() map[string]model.HealthState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	now := time.Now().UnixMilli()
	res := make(map[string]model.HealthState)
	for k, v := range m.health {
		if !v.IsHealthy && now > v.BlockUntil {
			continue
		}
		res[k] = *v
	}
	return res
}
