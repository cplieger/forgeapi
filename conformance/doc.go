// Package conformance is forgeapi's conformance suite: one case per operation per
// product, driven by the internal/spec table alone. The table decides which cases
// exist, their price, route, refusing capability and departures. Every case
// expects one value on all four products, so a pass says the families normalize
// four wire shapes onto one answer.
//
// The cases run offline against an httptest server serving the recorded fixtures
// under testdata, or live against the instance an environment variable names. A
// live mutation leaves its sandbox repository as it found it.
//
// This is the package's only non-test file and it declares nothing, so
// internal/surface renders nothing for it and the golden surface file stays put.
package conformance
