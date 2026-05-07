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
//
// We deliberately do NOT bump session lastSeen from this proxy. Server→client
// frames flow continuously whenever DF redraws (water, blinking cursor, etc.),
// which would mean an idle browser window keeps a session alive forever.
// "Idle" is defined as no real user input; the frontend posts to
// /session/keepalive on actual key/mouse activity.
func proxyWebsocket(w http.ResponseWriter, r *http.Request, host string, port int) {
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
	proxy.ServeHTTP(w, r)
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
