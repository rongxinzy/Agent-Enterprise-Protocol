package runtime

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestProbeRequiresSuccessfulHTTPStatus(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(status)
	}))
	defer server.Close()
	if err := Probe(server.URL, time.Second); err != nil {
		t.Fatal(err)
	}
	status = http.StatusServiceUnavailable
	if err := Probe(server.URL, time.Second); err == nil {
		t.Fatal("Probe accepted an unavailable endpoint")
	}
}
