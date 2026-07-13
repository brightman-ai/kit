package tunnel

import (
	"strings"
	"testing"
)

// TestTunnelChildEnvStripsProxy — cloudflared 子进程必须拿到**剥掉全部 HTTP(S)/ALL 代理变量**
// 的环境。回归背景 (线上真事故): 主机 HTTPS_PROXY 指向一个死代理时, cloudflared 继承该变量,
// 把到 Cloudflare 边缘 (7844) 的连接塞进死代理 → 超时挂死, 表现为隧道一直 "Starting..." 卡死;
// 而直连边缘明明是通的。剥掉代理变量后 31s 内注册成功。这个不变量一旦被回归, 隧道就再挂。
func TestTunnelChildEnvStripsProxy(t *testing.T) {
	for _, key := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "all_proxy",
	} {
		t.Setenv(key, "http://192.168.33.1:7894")
	}
	// 一个无关变量应当保留 (只剥代理, 不是清空整个环境)。
	t.Setenv("TUNNEL_KEEPME", "yes")

	env := tunnelChildEnv()

	for _, kv := range env {
		name := kv
		if i := strings.IndexByte(kv, '='); i > 0 {
			name = kv[:i]
		}
		switch name {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy":
			t.Fatalf("代理变量未被剥掉: %s (cloudflared 会走死代理挂死)", kv)
		}
	}
	if !containsEnv(env, "TUNNEL_KEEPME=yes") {
		t.Fatalf("非代理变量被误删: 期望保留 TUNNEL_KEEPME=yes")
	}
}

func containsEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}
