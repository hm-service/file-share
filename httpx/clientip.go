package httpx

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// DefaultTrustedProxies 是 TRUSTED_PROXIES 的默认值：只信本机，同机反代开箱可用。
const DefaultTrustedProxies = "127.0.0.1/32,::1/128"

// Proxies 决定客户端 IP 从哪里取：只有连接的对端落在可信网段里（请求确实来自我们自己的
// 反向代理）才认反代头，否则一律用对端地址。这样即使服务被直接访问、有人自己伪造
// X-Forwarded-For，也拿不到「别人」的身份。
type Proxies struct {
	trusted []netip.Prefix
}

// NewProxies 解析逗号分隔的 IP / CIDR 列表；空串按默认值处理。
func NewProxies(spec string) (*Proxies, error) {
	if strings.TrimSpace(spec) == "" {
		spec = DefaultTrustedProxies
	}
	proxies := &Proxies{}
	for item := range strings.SplitSeq(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(item); err == nil {
			proxies.trusted = append(proxies.trusted, prefix.Masked())
			continue
		}
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q is neither an IP nor a CIDR", item)
		}
		proxies.trusted = append(proxies.trusted, netip.PrefixFrom(addr, addr.BitLen()))
	}
	return proxies, nil
}

// ClientIP 取真实客户端 IP。对端不可信 → 直接用对端地址；对端可信 → 从
// X-Forwarded-For 的最右端往左走，跳过可信网段里的每一跳，第一个不可信的就是客户端
// （反代用 $proxy_add_x_forwarded_for 时，客户端自己伪造的部分拼在左边、真实来源追加在右边，
// 所以右起第一个不可信地址伪造不了）。没有 XFF 时退回 X-Real-IP。
func (p *Proxies) ClientIP(r *http.Request) string {
	peer := peerIP(r.RemoteAddr)
	peerAddr, err := netip.ParseAddr(peer)
	if p == nil || err != nil || !p.isTrusted(peerAddr) {
		return peer
	}
	for _, item := range slices.Backward(splitForwarded(r.Header.Get("X-Forwarded-For"))) {
		addr, err := netip.ParseAddr(strings.TrimSpace(item))
		if err != nil {
			continue
		}
		if !p.isTrusted(addr) {
			return addr.Unmap().String()
		}
	}
	if addr, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return addr.Unmap().String()
	}
	return peer
}

func (p *Proxies) isTrusted(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range p.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// peerIP 取 RemoteAddr 里的主机部分；解析不出来就原样返回。
func peerIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(strings.Trim(host, "[]"))
	if err != nil {
		return host
	}
	return addr.Unmap().String()
}

func splitForwarded(header string) []string {
	if strings.TrimSpace(header) == "" {
		return nil
	}
	return strings.Split(header, ",")
}
