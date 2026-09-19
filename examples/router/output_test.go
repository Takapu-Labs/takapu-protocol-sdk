package main

import (
	"testing"

	"github.com/Takapu-Labs/takapu-protocol-sdk/pkg/router"
	pb "github.com/Takapu-Labs/takapu-protocol-sdk/pkg/types"
)

func TestApplicationCacheOwnershipAndInvalidation(t *testing.T) {
	o := quoteObserver{
		latest: make(map[router.FrameKey]router.FrameUpdate),
		seen:   make(map[router.FrameKey][32]byte),
	}
	status := router.ClientStatus{Phase: router.Connected, Generation: 1, SubscriptionRevision: 1}
	first := router.FrameUpdate{
		Key: router.FrameKey{ChainID: 1, PairID: 7}, Frame: &pb.MarketFrame{UpdatedAt: 10},
		Generation: 1, SubscriptionRevision: 1,
	}
	if err := o.accept(first, status, 1); err != nil {
		t.Fatal(err)
	}
	latest := first
	latest.Frame = &pb.MarketFrame{UpdatedAt: 11}
	if err := o.accept(latest, status, 1); err != nil || o.latest[first.Key].Frame != latest.Frame {
		t.Fatalf("same stream must replace the application snapshot: %v", err)
	}
	other := latest
	other.Key.PairID++
	if err := o.accept(other, status, 1); err == nil {
		t.Fatal("application cache must enforce its own capacity")
	}
	o.seen[first.Key] = [32]byte{1}
	status.SubscriptionRevision++
	if err := o.accept(latest, status, 1); err != nil || len(o.latest) != 0 || len(o.seen) != 0 {
		t.Fatal("subscription change must invalidate old cache and discard an in-flight old frame")
	}
	latest.SubscriptionRevision = status.SubscriptionRevision
	if err := o.accept(latest, status, 1); err != nil || len(o.latest) != 1 {
		t.Fatalf("new revision must be accepted: %v", err)
	}
	status.Phase = router.Reconnecting
	o.sync(status)
	if len(o.latest) != 0 {
		t.Fatal("disconnect must invalidate retained frames before reconnect")
	}
	status.Phase = router.Connected
	status.Generation++
	if err := o.accept(latest, status, 1); err != nil || len(o.latest) != 0 {
		t.Fatal("old-generation frame must not repopulate cache after reconnect")
	}
	latest.Generation = status.Generation
	if err := o.accept(latest, status, 1); err != nil || len(o.latest) != 1 {
		t.Fatalf("new generation must be accepted: %v", err)
	}
	status.Phase = router.Stopped
	o.sync(status)
	if len(o.latest) != 0 {
		t.Fatal("shutdown must invalidate retained frames")
	}
}
