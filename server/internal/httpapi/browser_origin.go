package httpapi

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
)

// CanonicalBrowserOrigin accepts a serialized HTTP(S) origin, rejects URL
// components that do not belong in an Origin header, and normalizes default
// ports. It is exported so process configuration and request enforcement use
// the same origin parser.
func CanonicalBrowserOrigin(raw string) (string, error) {
	parsed, err := parseBrowserURL(raw, false)
	if err != nil {
		return "", err
	}
	return serializeOrigin(parsed), nil
}

func requestSourceOrigin(c *gin.Context) (string, error) {
	if rawOrigin := strings.TrimSpace(c.GetHeader("Origin")); rawOrigin != "" {
		return CanonicalBrowserOrigin(rawOrigin)
	}
	referer := strings.TrimSpace(c.GetHeader("Referer"))
	if referer == "" {
		return "", errors.New("请求缺少 Origin 与 Referer")
	}
	parsed, err := parseBrowserURL(referer, true)
	if err != nil {
		return "", err
	}
	return serializeOrigin(parsed), nil
}

func parseBrowserURL(raw string, allowResourcePath bool) (*url.URL, error) {
	value := strings.TrimSpace(raw)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.Host == "" || parsed.User != nil {
		return nil, errors.New("浏览器来源必须是有效的 HTTP(S) URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("浏览器来源必须使用 HTTP(S)")
	}
	if !allowResourcePath && parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("浏览器 origin 不能包含路径")
	}
	if !allowResourcePath && (parsed.RawQuery != "" || parsed.Fragment != "") {
		return nil, errors.New("浏览器 origin 不能包含查询参数或片段")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" || strings.HasSuffix(hostname, ".") {
		return nil, errors.New("浏览器来源主机名无效")
	}
	return parsed, nil
}

func serializeOrigin(parsed *url.URL) string {
	hostname := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
		port = ""
	}
	host := hostname
	if port != "" {
		host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	return parsed.Scheme + "://" + host
}
