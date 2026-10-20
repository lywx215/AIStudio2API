package managementcontract

import "context"

// NativeLeaseProof is implemented by a live native production AccountLease.
// It is never built from HTTP input. NativeLeaseActive must become false on
// Release; epoch is assigned when the generation's pool is bound, not inferred
// from authGeneration. G2 supplies this proof only for in-flight native cookie,
// auth refresh or definitive authentication-state writes.
type NativeLeaseProof interface {
	NativeLeaseAccountID() string
	NativeLeaseEpoch() uint64
	NativeLeaseActive() bool
}

type nativeLeaseContextKey struct{}

func WithNativeLease(ctx context.Context, proof NativeLeaseProof) context.Context {
	return context.WithValue(ctx, nativeLeaseContextKey{}, proof)
}

func NativeLeaseFromContext(ctx context.Context) (NativeLeaseProof, bool) {
	proof, ok := ctx.Value(nativeLeaseContextKey{}).(NativeLeaseProof)
	return proof, ok && proof != nil && proof.NativeLeaseActive()
}

// NativeMutationRuntime is an additional required interface on G0's real runtime
// adapter. It avoids self-drain for an existing active production lease while
// retaining the coordinator's shared BindRuntime gate and the same epoch.
// It must not wait behind a management drain that is waiting for this lease.
// Native publication preserves the active worker; it must not reset or force
// interrupt the caller's stream. Commit/account file serialization still applies.
type NativeMutationRuntime interface {
	AcquireNativeMutation(context.Context, NativeLeaseProof) (AccountMutationLease, error)
}
