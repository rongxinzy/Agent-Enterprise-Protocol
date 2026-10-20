package repository

import (
	"errors"
	"slices"
	"testing"
	"time"
)

func TestWorkloadPodObservationPublicationWithPostgres(t *testing.T) {
	store, pool := sourceCaptureTestStore(t)
	seedSource(t, pool)
	seedWorkloadLicense(t, pool)
	w := workloadObservation()
	w.PodUIDs = []string{"pod-b", "pod-a"}
	key, err := store.RegisterModelAccessWorkload(t.Context(), "employee", "", w, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(readSource(t, store, "employee").Workloads[0].PodUIDs, []string{"pod-a", "pod-b"}) {
		t.Fatal("registry did not persist canonical Pod evidence")
	}
	first, policy := workloadPolicy(t, captureBindings(t, store, "employee"))
	if policy.Disabled || !slices.Equal(policy.PodUIDs, []string{"pod-a", "pod-b"}) {
		t.Fatal("producer lost live Pods")
	}
	version, _ := captureAcknowledgement(t, pool, "employee")
	peer, _ := captureAcknowledgement(t, pool, "alice")
	w.PodUIDs = []string{"pod-a", "pod-b"}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if after, _ := captureAcknowledgement(t, pool, "employee"); after != version {
		t.Fatal("equivalent set/renewal queued a change")
	}
	w.PodUIDs = []string{"pod-b", "pod-c"}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	if after, _ := captureAcknowledgement(t, pool, "employee"); after <= version {
		t.Fatal("Pod replacement failed to queue publication")
	}
	if after, _ := captureAcknowledgement(t, pool, "alice"); after != peer {
		t.Fatal("Pod replacement affected an unrelated user")
	}
	replacement, active := workloadPolicy(t, captureBindings(t, store, "employee"))
	if active.Disabled || replacement.BindingKey != key || replacement.Sequence <= first.Sequence ||
		replacement.ContentHash == first.ContentHash || !slices.Equal(active.PodUIDs, w.PodUIDs) {
		t.Fatal("Pod replacement did not produce a newer policy on the stable binding")
	}
	w.PodUIDs = nil
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	_, empty := workloadPolicy(t, captureBindings(t, store, "employee"))
	if empty.Disabled || empty.Retired || !slices.Equal(empty.Models, active.Models) || len(empty.PodUIDs) != 0 {
		t.Fatal("no-Pod state must preserve identity/model semantics; native membership guard denies")
	}
	w.PodUIDs = []string{"pod-recovered"}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, 2*time.Minute); err != nil {
		t.Fatal(err)
	}
	restored, allow := workloadPolicy(t, captureBindings(t, store, "employee"))
	if allow.Disabled || restored.BindingKey != key || !slices.Equal(allow.PodUIDs, w.PodUIDs) {
		t.Fatal("replacement Pod could not recover the existing employee")
	}
	w.PodUIDs = []string{"duplicate", "duplicate"}
	if _, err := store.RegisterModelAccessWorkload(t.Context(), "employee", key, w, 2*time.Minute); !errors.Is(err, ErrAccessBinding) {
		t.Fatal("duplicate evidence accepted", err)
	}
}
