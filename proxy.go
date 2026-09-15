package downloader

import (
	"errors"
	"net/http"
	"net/url"
)

// errBadProxyScheme 在代理 URL 使用了不支持的 scheme 时返回。
var errBadProxyScheme = errors.New("proxy scheme must be http, https, socks5, or socks5h")

// setProxy 为 tr 配置给定的代理 URL。URL 的 scheme 必须是
// http、https、socks5 或 socks5h。
func setProxy(tr *http.Transport, proxy string) error {
	u, err := url.Parse(proxy)
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return errBadProxyScheme
	}
	tr.Proxy = http.ProxyURL(u)
	return nil
}
