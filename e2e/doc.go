// Package e2e holds the path test: one ad walked along the team's real path
// (Spy, Create, the library, Launch, Intel) through the real binaries, with
// fakes in place of Taboola, OpenAI and Google Drive. See README.md.
//
// It imports no service's module: it builds each service's command and
// talks to it the way its pages and the other services do, over HTTP and
// the _api views.
package e2e
