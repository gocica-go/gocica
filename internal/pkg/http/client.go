package http

import (
	"net"
	"net/http"
	"time"
)

const (
	maxIdleConns        = 100000
	maxIdleConnsPerHost = 10000
	keepAliveTime       = 90 * time.Second
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
		Timeout:   30 * time.Second,
		KeepAlive: keepAliveTime,
	}).DialContext

	http.DefaultTransport = newTransport

	return &http.Client{
		Transport: newTransport,
	}
}
