package enginev1

// AotBlobsBundler is satisfied by engine getPayload responses that carry an
// ahead-of-time (AOT) blob bundle (Blob Streaming). It is the AOT counterpart to
// BlobsBundler, which covers the just-in-time (JIT) blob bundle. Each entry is a
// per-blob AotBlobBundleV1 (versioned hash, KZG commitment, and cell proofs).
type AotBlobsBundler interface {
	GetAotBlobBundle() []*AotBlobBundleV1
}
