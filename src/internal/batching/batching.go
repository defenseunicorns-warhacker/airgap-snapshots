// Package batching packs a backup's staged files into peat SendAttachments
// bundles that respect peat's per-bundle caps: a file-count limit
// (--attachment-max-files-per-bundle, default 64) AND a total-bytes limit
// (--attachment-max-bundle-bytes, default 1 GiB).
//
// It is a pure, deterministic function so bundle_ids derived from batch index
// are stable across reconciles (required for peat's idempotency contract).
package batching

import "github.com/defenseunicorns/snapback/internal/peat"

// Batch is one SendAttachments bundle.
type Batch struct {
	Files []peat.FileSpec
	Bytes uint64
}

// Pack groups files into batches under maxFiles and maxBytes. Files larger than
// maxBytes are placed in a batch of their own (peat enforces the per-file cap
// separately; oversized files surface as a peat error at send time). Input
// order is preserved so batch indices are deterministic.
func Pack(files []peat.FileSpec, maxFiles int, maxBytes uint64) []Batch {
	if maxFiles <= 0 {
		maxFiles = 64
	}
	if maxBytes == 0 {
		maxBytes = 1 << 30
	}

	var batches []Batch
	var cur Batch
	flush := func() {
		if len(cur.Files) > 0 {
			batches = append(batches, cur)
			cur = Batch{}
		}
	}

	for _, f := range files {
		wouldExceedFiles := len(cur.Files)+1 > maxFiles
		wouldExceedBytes := len(cur.Files) > 0 && cur.Bytes+f.Size > maxBytes
		if wouldExceedFiles || wouldExceedBytes {
			flush()
		}
		cur.Files = append(cur.Files, f)
		cur.Bytes += f.Size
	}
	flush()
	return batches
}
