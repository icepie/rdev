package client

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lxzan/gws"
)

func websocketDialerFor(rawURL string) func() (gws.Dialer, error) {
	return func() (gws.Dialer, error) {
		targetURL, err := url.Parse(rawURL)
		if err != nil {
			return nil, err
		}
		proxyCheckURL := *targetURL
		switch proxyCheckURL.Scheme {
		case "ws":
			proxyCheckURL.Scheme = "http"
		case "wss":
			proxyCheckURL.Scheme = "https"
		}
		proxyURL, err := http.ProxyFromEnvironment(&http.Request{URL: &proxyCheckURL})
		if err != nil {
			return nil, err
		}
		if proxyURL == nil {
			return &net.Dialer{Timeout: 10 * time.Second}, nil
		}
		if proxyURL.Scheme != "http" && proxyURL.Scheme != "https" {
			return nil, fmt.Errorf("unsupported proxy scheme %q", proxyURL.Scheme)
		}
		return &httpConnectDialer{proxyURL: proxyURL, timeout: 10 * time.Second}, nil
	}
}

type httpConnectDialer struct {
	proxyURL *url.URL
	timeout  time.Duration
}

func (d *httpConnectDialer) Dial(network, addr string) (net.Conn, error) {
	proxyAddr := canonicalProxyAddr(d.proxyURL)
	conn, err := (&net.Dialer{Timeout: d.timeout}).Dial(network, proxyAddr)
	if err != nil {
		return nil, err
	}
	if d.proxyURL.Scheme == "https" {
		serverName := d.proxyURL.Hostname()
		tlsConn := tls.Client(conn, &tls.Config{ServerName: serverName})
		if err := tlsConn.Handshake(); err != nil {
			conn.Close()
			return nil, err
		}
		conn = tlsConn
	}

	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	if d.proxyURL.User != nil {
		password, _ := d.proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(d.proxyURL.User.Username() + ":" + password))
		req.Header.Set("Proxy-Authorization", "Basic "+token)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT %s returned %s", addr, resp.Status)
	}
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

func canonicalProxyAddr(proxyURL *url.URL) string {
	host := proxyURL.Host
	if strings.Contains(host, ":") {
		return host
	}
	if proxyURL.Scheme == "https" {
		return net.JoinHostPort(host, "443")
	}
	return net.JoinHostPort(host, "80")
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.reader != nil && c.reader.Buffered() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}
