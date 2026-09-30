// Package web holds the page script funnels-edge serves at /ah.js: the
// beacon and the VSL player, one file.
package web

import _ "embed"

// Script is ah.js as it is served.
//
//go:embed ah.js
var Script []byte
