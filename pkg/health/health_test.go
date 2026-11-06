package health

import (
	"testing"

	"edgetunnel-lb/pkg/config"
)

func TestHealthCircuitBreaker(t *testing.T) {
	cfgMgr := &config.Manager{}
	mgr := InitHealthManager(cfgMgr, nil)

	backend := "test-worker.example.workers.dev"

	// 连续 4 次失败不应触发熔断
	for i := 0; i < FailureThreshold-1; i++ {
		mgr.MarkBackendFailure(backend, "HTTP 502")
	}

	healthy := mgr.GetHealthyBackends([]string{backend}, nil, nil)
	if len(healthy) != 1 || healthy[0] != backend {
		t.Fatalf("连续 %d 次失败不应被熔断隔离，但已被过滤: %v", FailureThreshold-1, healthy)
	}

	// 第 5 次失败应触发熔断隔离
	mgr.MarkBackendFailure(backend, "HTTP 502")
	healthy = mgr.GetHealthyBackends([]string{backend, "backup-worker.example.workers.dev"}, nil, nil)
	if len(healthy) != 1 || healthy[0] == backend {
		t.Fatalf("达到阈值后未被熔断隔离: %v", healthy)
	}

	// 恢复成功
	mgr.MarkBackendSuccess(backend)
	healthy = mgr.GetHealthyBackends([]string{backend, "backup-worker.example.workers.dev"}, nil, nil)
	if len(healthy) != 2 {
		t.Fatalf("恢复成功后未能重新进入健康池: %v", healthy)
	}
}

func TestHealthNetworkTransportErrorIgnored(t *testing.T) {
	cfgMgr := &config.Manager{}
	mgr := InitHealthManager(cfgMgr, nil)

	backend := "transport-err-worker.example.workers.dev"

	// 网络底层超时或连接重置不归咎于后端
	for i := 0; i < 10; i++ {
		mgr.MarkBackendFailure(backend, "context deadline exceeded (i/o timeout)")
	}

	healthy := mgr.GetHealthyBackends([]string{backend}, nil, nil)
	if len(healthy) != 1 {
		t.Fatalf("网络层异常不应导致后端被熔断: %v", healthy)
	}
}

func TestHealthSnapshot(t *testing.T) {
	cfgMgr := &config.Manager{}
	mgr := InitHealthManager(cfgMgr, nil)

	backend := "stat-worker.example.workers.dev"
	mgr.RecordBackendReq(backend, true)
	mgr.RecordBackendReq(backend, true)
	mgr.RecordBackendReq(backend, false)

	stats := mgr.GetStatsSnapshot()
	s, ok := stats[backend]
	if !ok {
		t.Fatalf("缺少后端统计信息: %s", backend)
	}
	if s.Total != 3 || s.Success != 2 || s.Fail != 1 {
		t.Errorf("统计数据不准确: %+v", s)
	}
}
