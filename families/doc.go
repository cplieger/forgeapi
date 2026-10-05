// Package families opens a client for whichever forge family answers at a
// connection.
//
// It is the one package that imports all three family packages: the root package
// imports none, so a consumer can depend on the vocabulary alone, and a registry
// filled by package initializers is a global this library does not build. A
// consumer that knows its family calls that family's constructor and pays nothing
// for detection.
//
// [Open] answers under the root package's three-way error contract: nil, a
// [*forgeapi.Error], or a context sentinel returned unchanged. Detection's own
// failure is [forgeapi.CodeFamilyUndetected], and it is not a context failure.
package families
