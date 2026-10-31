package main

import (
	"testing"
	"time"
)

func TestChainHashDetectsTampering(t *testing.T) {
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	e1 := &Event{Sequence: 1, EventID: "e1", ActorID: "u1", Service: "transfer-service", Action: "transfer.completed", AfterJSON: `{"amount":100}`, OccurredAt: at}
	e1.Hash = chainHash(genesisHash, e1)
	e2 := &Event{Sequence: 2, EventID: "e2", ActorID: "u2", Service: "kyc-service", Action: "kyc.review", OccurredAt: at}
	e2.Hash = chainHash(e1.Hash, e2)

	if e1.Hash == e2.Hash || len(e1.Hash) != 64 {
		t.Fatal("hashes should be distinct sha256 hex")
	}
	// Editing any field of an earlier event changes its hash, so the next link breaks.
	tampered := *e1
	tampered.AfterJSON = `{"amount":1000000}`
	if chainHash(genesisHash, &tampered) == e1.Hash {
		t.Fatal("tampering not detected")
	}
	// Reordering breaks the chain too.
	if chainHash(genesisHash, e2) == e2.Hash {
		t.Fatal("reordering not detected")
	}
}
