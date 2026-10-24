package dialer

import (
	"context"
	"testing"
	"time"
)

func TestSetAndGetDNS(t *testing.T) {
	SetDNS([]string{"223.5.5.5", "119.29.29.29:53"})
	servers := GetDNS()
	if len(servers) != 2 {
		t.Fatalf("期望 2 个 DNS，实际获取到 %d 个: %v", len(servers), servers)
	}
	if servers[0] != "223.5.5.5:53" || servers[1] != "119.29.29.29:53" {
		t.Errorf("DNS 格式化异常: %v", servers)
	}
}

func TestResolveClean(t *testing.T) {
	resolver := GetDefaultResolver()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ips, err := resolver.ResolveClean(ctx, "cloudflare.com")
	if err != nil {
		t.Logf("ResolveClean 测试跳过 (网络可能无法连接公网 DNS): %v", err)
		return
	}
	if len(ips) == 0 {
		t.Errorf("解析结果不应为空")
	}
}

