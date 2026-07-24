// Package archtest holds architecture-conformance tests. They parse the
// module-internal import graph (no external tooling, no build tags) and fail if
// a guarded package gains a dependency that would invert the intended layering:
// concrete backends, config, and the durability implementation depend on the
// core abstractions, never the reverse.
package archtest
