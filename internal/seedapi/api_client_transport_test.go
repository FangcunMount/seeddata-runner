package seedapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/FangcunMount/component-base/pkg/log"
)

func TestAPIClientUsesSharedHighConcurrencyTransport(t *testing.T) {
	first := NewAPIClient("http://example.test", "token", log.New(log.NewOptions()))
	second := NewAPIClient("http://example.test", "token", log.New(log.NewOptions()))
	if first.httpClient.Transport != second.httpClient.Transport {
		t.Fatal("API clients do not share the connection pool")
	}
	transport, ok := first.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport type %T", first.httpClient.Transport)
	}
	if transport.MaxIdleConns != 128 || transport.MaxIdleConnsPerHost != 64 || transport.MaxConnsPerHost != 64 || transport.IdleConnTimeout != 90*time.Second {
		t.Fatalf("unexpected transport limits: %+v", transport)
	}
}
