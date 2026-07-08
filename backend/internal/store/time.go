package store

import "time"

// Time aliases time.Time; kept as a named type so a future switch to
// timestamptz-with-zone handling stays a one-file change.
type Time = time.Time
