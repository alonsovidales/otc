// Fixture: a device package whose errors are internal: only sinks count.
package dao

import "errors"

var errClosed = errors.New("the database is closed for now")

type Ack struct{ ErrorMsg string }

func ack() *Ack { return &Ack{ErrorMsg: "Too many attempts. Try again later."} }
