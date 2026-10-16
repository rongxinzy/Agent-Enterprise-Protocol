package dataplane

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	core "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PodDump reads only /config_dump using the standard Kubernetes port-forward
// transport. Envoy admin stays on loopback; no exec or public admin listener.
// Do not wrap transport errors: they can contain remote response bodies.
func PodDump(config *rest.Config, client kubernetes.Interface) func(context.Context, core.Pod) ([]byte, error) {
	return func(ctx context.Context, pod core.Pod) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		transport, upgrader, err := spdy.RoundTripperFor(config)
		if err != nil {
			return nil, errUnsupported
		}
		endpoint := client.CoreV1().RESTClient().Post().Namespace(pod.Namespace).Resource("pods").Name(pod.Name).SubResource("portforward").URL()
		dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport, Timeout: 5 * time.Second}, http.MethodPost, endpoint)
		stop, ready := make(chan struct{}), make(chan struct{})
		forwarder, err := portforward.NewOnAddresses(dialer, []string{"127.0.0.1"}, []string{"0:15000"}, stop, ready, io.Discard, io.Discard)
		if err != nil {
			return nil, errUnsupported
		}
		finished := make(chan error, 1)
		go func() { finished <- forwarder.ForwardPorts() }()
		defer close(stop)
		select {
		case <-ctx.Done():
			return nil, errUnsupported
		case <-finished:
			return nil, errUnsupported
		case <-ready:
		}
		ports, err := forwarder.GetPorts()
		if err != nil || len(ports) != 1 {
			return nil, errUnsupported
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/config_dump", ports[0].Local), nil)
		if err != nil {
			return nil, errUnsupported
		}
		transportHTTP := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 32 << 10}
		defer transportHTTP.CloseIdleConnections()
		httpClient := &http.Client{Transport: transportHTTP, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, errUnsupported
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, errUnsupported
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, MaxDumpBytes+1))
		if err != nil || len(body) > MaxDumpBytes {
			return nil, errUnsupported
		}
		return body, nil
	}
}
