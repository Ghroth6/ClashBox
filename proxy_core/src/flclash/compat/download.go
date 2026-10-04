package compat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/metacubex/http"
	coreDialer "github.com/metacubex/mihomo/component/dialer"
	coreForwarding "github.com/metacubex/mihomo/component/forwarding"
	"github.com/metacubex/mihomo/component/resource"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

func DownloadConfig(parent context.Context, url string, userAgent string, filePath string) (string, error) {
	rawURL := strings.TrimSpace(url)
	userAgent = strings.TrimSpace(userAgent)
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", fmt.Errorf("filePath is empty")
	}
	parent, finish, err := coreForwarding.AcquireManagementNetwork(parent)
	if err != nil {
		return "", err
	}
	defer finish()

	ctx, cancel := context.WithTimeout(parent, time.Second*20)
	defer cancel()

	parsedURL, authHeader, err := normalizeDownloadURL(rawURL)
	if err != nil {
		return "", err
	}
	// 不使用系统http, 因为系统http在某些机场配置返回403
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL, nil)
	if err != nil {
		return "", err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	} else {
		req.Header.Set("User-Agent", "clash-verge/v2.5.1")
	}

	client := &http.Client{
		Timeout:       time.Second * 20,
		Transport:     newDownloadTransport(ctx),
		CheckRedirect: limitDownloadRedirects,
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("download config failed: %s %s", resp.Status, limitErrorBody(data))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := resource.Commit(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return os.WriteFile(filePath, data, 0o644)
	}); err != nil {
		return "", err
	}

	result := map[string]string{
		"content-disposition":   resp.Header.Get("Content-Disposition"),
		"subscription-userinfo": getSubscriptionUserInfo(resp.Header),
	}
	resultJson, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(resultJson), nil
}

func normalizeDownloadURL(rawURL string) (string, string, error) {
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("failed to parse subscription URL: %w", err)
	}

	if parsedURL.RawQuery == "" && strings.Contains(parsedURL.Path, "&") {
		path, dirtyParams, _ := strings.Cut(parsedURL.Path, "&")
		parsedURL.Path = path
		parsedURL.RawPath = ""
		parsedURL.RawQuery = dirtyParams
	}

	authHeader := ""
	if parsedURL.User != nil && parsedURL.User.Username() != "" {
		username, _ := url.PathUnescape(parsedURL.User.Username())
		password, _ := parsedURL.User.Password()
		password, _ = url.PathUnescape(password)
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
		parsedURL.User = nil
	}

	return parsedURL.String(), authHeader, nil
}

func newDownloadTransport(parent context.Context) *http.Transport {
	protected := &net.Dialer{
		Timeout:   time.Second * 20,
		KeepAlive: time.Second * 60,
		ControlContext: func(ctx context.Context, network, address string, raw syscall.RawConn) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if coreDialer.DefaultSocketHook != nil {
				return coreDialer.DefaultSocketHook(network, address, raw)
			}
			return nil
		},
	}
	// DNS sockets need the same platform protection as the HTTP connection.
	dialer := *protected
	dialer.Resolver = &net.Resolver{PreferGo: true, Dial: managementDownloadDial(parent, protected.DialContext)}
	return &http.Transport{
		Proxy:               nil,
		DialContext:         managementDownloadDial(parent, dialer.DialContext),
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: time.Second * 10,
	}
}

func managementDownloadDial(parent context.Context, dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(request context.Context, network, address string) (net.Conn, error) {
		// HTTP transports can detach their dial context from request cancellation.
		// Keep every DNS/TCP construction tied to the original network epoch and
		// register the returned physical socket before releasing construction.
		ctx, finish, err := coreForwarding.AcquireManagementNetwork(parent)
		if err != nil {
			return nil, err
		}
		defer finish()
		ctx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(request, cancel)
		defer func() { stop(); cancel() }()
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return coreForwarding.OwnNetworkConn(ctx, conn)
	}
}

func limitDownloadRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

func getSubscriptionUserInfo(header http.Header) string {
	for key, values := range header {
		keyLower := strings.ToLower(key)
		if keyLower == "subscription-userinfo" || strings.HasSuffix(keyLower, "-subscription-userinfo") {
			if len(values) > 0 {
				return values[0]
			}
			return ""
		}
	}
	return ""
}

func limitErrorBody(data []byte) string {
	body := strings.TrimSpace(string(data))
	if body == "" {
		return ""
	}
	if len(body) > 512 {
		body = body[:512]
	}
	return body
}
