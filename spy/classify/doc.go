// Package classify puts each creative in one vertical of Spy's fixed list
// (spy/verticals/verticals.yaml), or in none. Two passes, as in the old
// collector: keyword rules read the headlines, brands and landing page
// titles; a small model, trained each day on the creatives the rules are
// sure about, answers for those they are not. Neither may answer with
// anything but an id from the list. No paid API.
package classify
