// Copyright 2026 Defense Unicorns
// SPDX-License-Identifier: AGPL-3.0-or-later OR LicenseRef-Defense-Unicorns-Commercial

package batching

import (
	"testing"

	"github.com/defenseunicorns/snapback/internal/peat"
)

func files(sizes ...uint64) []peat.FileSpec {
	out := make([]peat.FileSpec, len(sizes))
	for i, s := range sizes {
		out[i] = peat.FileSpec{RelativePath: "k", Size: s}
	}
	return out
}

func TestPack_FileCountCap(t *testing.T) {
	got := Pack(files(1, 1, 1, 1, 1), 2, 1<<30)
	if len(got) != 3 {
		t.Fatalf("want 3 batches, got %d", len(got))
	}
	if len(got[0].Files) != 2 || len(got[2].Files) != 1 {
		t.Fatalf("unexpected batch sizes: %+v", got)
	}
}

func TestPack_ByteCap(t *testing.T) {
	got := Pack(files(600, 600, 600), 64, 1000)
	if len(got) != 3 {
		t.Fatalf("want 3 batches (each 600 > 1000 cap when paired), got %d", len(got))
	}
}

func TestPack_OversizeFileGetsOwnBatch(t *testing.T) {
	got := Pack(files(2000, 10), 64, 1000)
	if len(got) != 2 {
		t.Fatalf("want 2 batches, got %d: %+v", len(got), got)
	}
	if got[0].Bytes != 2000 || got[1].Bytes != 10 {
		t.Fatalf("unexpected packing: %+v", got)
	}
}

func TestPack_Empty(t *testing.T) {
	if got := Pack(nil, 64, 1<<30); len(got) != 0 {
		t.Fatalf("want 0 batches, got %d", len(got))
	}
}

func TestPack_Deterministic(t *testing.T) {
	in := files(100, 200, 300, 400, 500)
	a := Pack(in, 2, 1<<30)
	b := Pack(in, 2, 1<<30)
	if len(a) != len(b) {
		t.Fatalf("non-deterministic batch count")
	}
	for i := range a {
		if a[i].Bytes != b[i].Bytes || len(a[i].Files) != len(b[i].Files) {
			t.Fatalf("non-deterministic packing at %d", i)
		}
	}
}
