package dataplane

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestPortForwardRejectsUnauthorizedTransportWithoutResponseLeak(t *testing.T) {
	seen := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Method + " " + r.URL.Path
		http.Error(w, "sensitive-upstream-diagnostic", http.StatusForbidden)
	}))
	defer server.Close()
	config := &rest.Config{Host: server.URL}
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	dump := PodDump(config, kube)
	_, err = dump(t.Context(), core.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "higress-system", Name: "gateway"}})
	if err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("transport failure not safely rejected")
	}
	if got := <-seen; got != "POST /api/v1/namespaces/higress-system/pods/gateway/portforward" {
		t.Fatal("unexpected API surface")
	}
}
