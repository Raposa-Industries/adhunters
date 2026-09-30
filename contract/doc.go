// Package contract is the data contract between services: the views each
// service publishes in its <svc>_api schema, as SQL and as Go row types.
//
// A service reads another service's data only through these views and asks
// it to do things only through its _api functions. A view never changes shape
// in place; a new shape is a new version (tracks_api.ad_hourly_v2 beside _v1),
// and CI fails when a published view changes without one.
//
// sql/tracks holds tracks_api's views and functions, one file each. Tracks'
// migrations create them word for word, and a test there checks it.
//
// actions/ is the action catalog: what each app lets a teammate do, as the
// views and functions above, so Desk can do the same with the same rights.
package contract
