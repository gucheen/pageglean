package bookmarks

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

const MaxURLLength = 8192

func NormalizeURL(value string) (original string, canonical string, err error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", "", fmt.Errorf("URL 不能为空")
	}
	if len(value) > MaxURLLength {
		return "", "", fmt.Errorf("URL 过长")
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", "", fmt.Errorf("URL 格式无效")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", "", fmt.Errorf("只支持 HTTP 和 HTTPS URL")
	}
	if parsed.Hostname() == "" {
		return "", "", fmt.Errorf("URL 缺少域名")
	}
	if parsed.User != nil {
		return "", "", fmt.Errorf("URL 不能包含用户名或密码")
	}

	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	parsed.Host = host
	parsed.Fragment = ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return value, parsed.String(), nil
}
