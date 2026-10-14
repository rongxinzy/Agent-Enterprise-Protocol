package main

import (
	"testing"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
)

func TestManagerOptions(t *testing.T) {
	options := managerOptions()
	if !options.LeaderElection {
		t.Fatal("leader election must be on by default")
	}
	if options.LeaderElectionID != "gateway-operator.gateway.aep.zhiyuan.io" {
		t.Fatalf("LeaderElectionID = %q", options.LeaderElectionID)
	}
	if options.LeaderElectionNamespace != "aep-system" {
		t.Fatalf("LeaderElectionNamespace = %q", options.LeaderElectionNamespace)
	}
	if !options.LeaderElectionReleaseOnCancel {
		t.Fatal("the lease must be released on a clean shutdown")
	}
	if options.Metrics.BindAddress != ":8092" || options.HealthProbeBindAddress != ":8093" {
		t.Fatalf("unexpected bind addresses: %#v", options)
	}
	if options.Scheme == nil {
		t.Fatal("scheme is nil")
	}
}

func TestWireManagerRegistersControllersAndHealth(t *testing.T) {
	// The manager is constructed without ever dialing the API server.
	mgr, err := ctrl.NewManager(&rest.Config{Host: "localhost:59090"}, managerOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := wireManager(mgr); err != nil {
		t.Fatal(err)
	}
}
