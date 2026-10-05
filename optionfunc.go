package forgeapi

// optionFunc adapts a mutator to [Option]. One adapter serves every With*
// function in this package, which is what keeps each option's whole content the
// clause it sets.
type optionFunc func(*Settings)

func (f optionFunc) applyOption(s *Settings) { f(s) }

// listOptionFunc adapts a mutator to [ListOption].
type listOptionFunc func(*ListSettings)

func (f listOptionFunc) applyListOption(s *ListSettings) { f(s) }

// nameUnknown is the spelling every enumeration's zero member renders as, which is
// the honest answer for an unpopulated field rather than a wrong one.
const nameUnknown = "unknown"

// stateOpenName, stateClosedName and stateMergedName are the spellings three of this
// library's state enumerations share, so one word is declared once rather than per
// enumeration.
const (
	stateOpenName   = "open"
	stateClosedName = "closed"
	stateMergedName = "merged"
)

// nameOf returns the member's spelling from a table, and the table's first entry
// for a value outside it, which is every enumeration's unknown member: a value
// nothing mapped renders as the honest unknown rather than as a number.
func nameOf[T ~int](names []string, v T) string {
	if int(v) < 0 || int(v) >= len(names) {
		return names[0]
	}
	return names[int(v)]
}
