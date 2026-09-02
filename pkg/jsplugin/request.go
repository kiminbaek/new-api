package jsplugin

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// ValidateRequestURL prevents plugins from directing a channel credential to
// hosts other than the configured base URL or an administrator-approved host.
func ValidateRequestURL(requestURL, baseURL string, allowedHosts []string) error {
	request, err := parsePluginHTTPURL(requestURL)
	if err != nil {
		return err
	}
	base, err := parsePluginHTTPURL(baseURL)
	if err != nil {
		return fmt.Errorf("channel base URL is invalid: %w", err)
	}
	if strings.EqualFold(base.Scheme, "https") && !strings.EqualFold(request.Scheme, "https") {
		return fmt.Errorf("plugin request URL must not downgrade HTTPS credentials")
	}
	requestHost := canonicalHost(request)
	if requestHost == canonicalHost(base) {
		return nil
	}
	for _, allowed := range allowedHosts {
		allowedURL, parseErr := url.Parse("https://" + strings.TrimSpace(allowed))
		if parseErr == nil && requestHost == canonicalHost(allowedURL) {
			return nil
		}
	}
	return fmt.Errorf("plugin request host %q is not allowed", request.Host)
}

// ValidateCredentiallessRequest permits an absolute cross-origin HTTP(S)
// request only when it cannot carry credentials or a request body.
func ValidateCredentiallessRequest(requestURL, method string, headers map[string]string, body any) error {
	if _, err := parsePluginHTTPURL(requestURL); err != nil {
		return err
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodHead {
		return fmt.Errorf("credentialless plugin requests must use GET or HEAD")
	}
	if len(headers) != 0 || body != nil {
		return fmt.Errorf("credentialless plugin requests cannot contain headers or a body")
	}
	return nil
}

// ValidateRequestHeaders rejects malformed, authority-changing, framing, and
// hop-by-hop headers before a plugin descriptor reaches net/http.
func ValidateRequestHeaders(headers map[string]string) error {
	if len(headers) > 64 {
		return fmt.Errorf("plugin request has too many headers")
	}
	for name, value := range headers {
		name = strings.TrimSpace(name)
		if !httpguts.ValidHeaderFieldName(name) ||
			!httpguts.ValidHeaderFieldValue(value) || len(value) > 8192 {
			return fmt.Errorf("plugin request contains an invalid header")
		}
		switch strings.ToLower(name) {
		case "host", "content-length", "accept-encoding", "connection",
			"proxy-connection", "keep-alive", "proxy-authorization", "te",
			"trailer", "transfer-encoding", "upgrade":
			return fmt.Errorf("plugin request header %q is not allowed", name)
		}
	}
	return nil
}

func parsePluginHTTPURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if raw != trimmed {
		return nil, fmt.Errorf("plugin request URL must not contain surrounding whitespace")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed == nil || parsed.Host == "" ||
		(!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) {
		return nil, fmt.Errorf("plugin request URL must be absolute HTTP(S)")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("plugin request URL must not contain userinfo")
	}
	if parsed.Fragment != "" {
		return nil, fmt.Errorf("plugin request URL must not contain a fragment")
	}
	return parsed, nil
}

func canonicalHost(value *url.URL) string {
	host := strings.ToLower(strings.TrimSuffix(value.Hostname(), "."))
	port := value.Port()
	if port == "" || port == "80" && value.Scheme == "http" || port == "443" && value.Scheme == "https" {
		return host
	}
	return net.JoinHostPort(host, port)
}
