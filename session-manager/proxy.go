package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// proxyWebsocket reverse-proxies the current request to the container's websocket port.
// onActivity is called on each write to track idle time.
func proxyWebsocket(w http.ResponseWriter, r *http.Request, hostPort int, onActivity func()) {
	if err := waitPort(hostPort, 10*time.Second); err != nil {
		http.Error(w, "container not ready in time", http.StatusGatewayTimeout)
		return
	}

	target := &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("127.0.0.1:%d", hostPort),
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

func waitPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return fmt.Errorf("port %d not ready after %s", port, timeout)
}
