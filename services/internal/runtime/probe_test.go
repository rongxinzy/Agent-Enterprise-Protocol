package runtime

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestProbeRejectsInvalidURLAndTimeout(t *testing.T) {
	if err := Probe("://invalid", time.Second); err == nil {
		t.Fatal("Probe accepted an invalid URL")
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	if err := Probe(server.URL, 20*time.Millisecond); err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("Probe timeout error = %v", err)
	}
	if err := Probe("http://127.0.0.1:0", time.Second); err == nil {
		t.Fatalf("Probe connection error = %v", err)
	}
}
