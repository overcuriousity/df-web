package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// proxyHTTPStream reverse-proxies the current request to an HTTP endpoint in the
// container, streaming the response body immediately (no response buffering).
func proxyHTTPStream(w http.ResponseWriter, r *http.Request, host string, port int) {
	if err := waitPort(host, port, 10*time.Second); err != nil {
		http.Error(w, "audio not ready", http.StatusGatewayTimeout)
		return
	}
	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("%s:%d", host, port)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1 // stream each write immediately
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("audio proxy error: %v", err)
		http.Error(w, "audio proxy error", http.StatusBadGateway)
	}
	proxy.ServeHTTP(w, r)
}

// proxyWebsocket reverse-proxies the current request to the container's websocket port.
// onActivity is called on each write to track idle time.
func proxyWebsocket(w http.ResponseWriter, r *http.Request, host string, port int, onActivity func()) {
	if err := waitPort(host, port, 10*time.Second); err != nil {
		http.Error(w, "container not ready in time", http.StatusGatewayTimeout)
		return
	}

	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s:%d", host, port),
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("proxy error: %v", err)
		http.Error(w, "proxy error", http.StatusBadGateway)
	}
	proxy.ServeHTTP(&activityWriter{ResponseWriter: w, fn: onActivity}, r)
}

type activityWriter struct {
	http.ResponseWriter
	fn func()
}

func (a *activityWriter) Write(b []byte) (int, error) {
	a.fn()
	return a.ResponseWriter.Write(b)
}

func (a *activityWriter) Flush() {
	if f, ok := a.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (a *activityWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return a.ResponseWriter.(http.Hijacker).Hijack()
}

func waitPort(host string, port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("%s:%d", host, port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("%s not ready after %s", addr, timeout)
}
