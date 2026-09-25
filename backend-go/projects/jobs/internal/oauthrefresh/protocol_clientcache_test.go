package oauthrefresh

// clientFor 代理 client 复用回归：同一 proxy URL 复用同一 *http.Client 实例
//（连接池随 Transport 复用），不同 proxy 各自独立，直连 fallback 与无效
// proxy 报错行为不变，缓存条数有上界。

import (
	"fmt"
	"testing"
)

func TestClientForReusesSameProxyClient(t *testing.T) {
	exchanger := NewHTTPTokenExchanger()
	proxy := "socks5://127.0.0.1:1080"

	first, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: proxy})
	if err != nil {
		t.Fatal(err)
	}
	second, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: proxy})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("同一 proxy 必须复用同一 client 实例")
	}
	// proxy URL 原样作为缓存键（不 trim 语义变化：空白 proxy 走直连 fallback）。
	third, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: proxy})
	if err != nil || third != first {
		t.Fatalf("三次请求 client 不一致: %v", err)
	}
}

func TestClientForSeparatesProxyClients(t *testing.T) {
	exchanger := NewHTTPTokenExchanger()
	socks, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: "socks5://127.0.0.1:1080"})
	if err != nil {
		t.Fatal(err)
	}
	httpProxy, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	if socks == httpProxy {
		t.Fatal("不同 proxy 必须各自独立 client")
	}
	// 既有条目不受新条目影响。
	again, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: "socks5://127.0.0.1:1080"})
	if err != nil || again != socks {
		t.Fatalf("已有条目必须继续复用: %v", err)
	}
}

func TestClientForDirectFallbackUnchanged(t *testing.T) {
	exchanger := NewHTTPTokenExchanger()
	direct, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: ""})
	if err != nil || direct != exchanger.Client {
		t.Fatalf("空 proxy 必须回退配置 client: %v", err)
	}
	// 零值结构（nil map、nil Client）同样工作。
	zero := &HTTPTokenExchanger{}
	zeroDirect, err := zero.clientFor(TokenHTTPRequest{})
	if err != nil || zeroDirect == nil {
		t.Fatalf("零值 exchanger 直连回退: %v", err)
	}
	zeroProxy, err := zero.clientFor(TokenHTTPRequest{ProxyURL: "socks5://127.0.0.1:1080"})
	if err != nil || zeroProxy == nil {
		t.Fatalf("零值 exchanger 代理路径: %v", err)
	}
	// 无效 proxy URL 保持报错（不静默直连）。
	if _, err := zero.clientFor(TokenHTTPRequest{ProxyURL: "://bad"}); err == nil {
		t.Fatal("无效 proxy 必须报错")
	}
}

func TestClientForCacheBounded(t *testing.T) {
	exchanger := NewHTTPTokenExchanger()
	for i := 0; i < maxProxyClients; i++ {
		proxy := fmt.Sprintf("socks5://10.0.0.%d:1080", i+1)
		if _, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: proxy}); err != nil {
			t.Fatal(err)
		}
	}
	if len(exchanger.proxyClients) != maxProxyClients {
		t.Fatalf("缓存条数 = %d", len(exchanger.proxyClients))
	}
	// 超上限的新代理不被缓存：两次调用得到不同实例（用后即弃，不淘汰既有条目）。
	overflow := "socks5://10.9.9.9:1080"
	first, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: overflow})
	if err != nil {
		t.Fatal(err)
	}
	second, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: overflow})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("超上限代理不应入缓存（两次必须新建）")
	}
	if len(exchanger.proxyClients) != maxProxyClients {
		t.Fatalf("超上限后缓存条数 = %d", len(exchanger.proxyClients))
	}
	// 既有条目不受影响。
	proxy := "socks5://10.0.0.1:1080"
	cached, err := exchanger.clientFor(TokenHTTPRequest{ProxyURL: proxy})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := exchanger.proxyClients[proxy]; !ok || cached != exchanger.proxyClients[proxy] {
		t.Fatal("既有条目必须继续复用")
	}
}
