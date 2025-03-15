package http

import (
	"net"
	"net/http"
	"time"
)

const (
	maxIdleConns        = 100000
	maxIdleConnsPerHost = 10000
	timeout             = 30 * time.Second
	keepAliveTime       = 1 * time.Hour
)

func NewClient() *http.Client {
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultClient
	}
	newTransport := defaultTransport.Clone()

	newTransport.MaxIdleConns = maxIdleConns
	newTransport.MaxIdleConnsPerHost = maxIdleConnsPerHost
	newTransport.DialContext = (&net.Dialer{
		Timeout:   timeout,
		KeepAlive: keepAliveTime,
	}).DialContext

	return &http.Client{
		Transport: newTransport,
	}
}
