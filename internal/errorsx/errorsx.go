package errorsx

import (
	"fmt"
	"log"
	"time"
	// "github.com/egdaemon/egmeta/internal/langx"
)

// Zero logs that the error occurred but otherwise ignores it.
func Zero[T any](v T, err error) T {
	if err == nil {
		return v
	}

	if cause := log.Output(2, fmt.Sprintln(err)); cause != nil {
		panic(cause)
	}

	return v
}

func Log(err error) {
	if err == nil {
		return
	}

	if cause := log.Output(2, fmt.Sprintln(err)); cause != nil {
		panic(cause)
	}
}

func Must[T any](v T, err error) T {
	if err == nil {
		return v
	}

	panic(err)
}

// panic if zero value.
func PanicZero[T comparable](v T) T {
	var (
		x T
	)

	if v == x {
		panic("zero value detected")
	}

	return v
}

// Compact returns the first error in the set, if any.
func Compact(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}

	return nil
}

// String useful wrapper for string constants as errors.
type String string

func (t String) Error() string {
	return string(t)
}

func Authorization(cause error) error {
	return unauthorized{
		error: cause,
	}
}

type Unauthorized interface {
	Unauthorized()
}

type unauthorized struct {
	error
}

func (t unauthorized) Unauthorized() {

}

// Timeout error.
type Timeout interface {
	Timedout() time.Duration
}

// Timedout represents a timeout.
func Timedout(cause error, d time.Duration) error {
	return timeout{
		error: cause,
		d:     d,
	}
}

type timeout struct {
	error
	d time.Duration
}

func (t timeout) Timedout() time.Duration {
	return t.d
}

// Notification presents an error that will be displayed to the user
// to provide notifications.
func Notification(err error) error {
	return notification{
		error: err,
	}
}

type notification struct {
	error
}

func (t notification) Notification() {}
func (t notification) Unwrap() error {
	return t.error
}
func (t notification) Cause() error {
	return t.error
}

// UserFriendly represents an error that will be displayed to users.
func UserFriendly(err error) error {
	return userfriendly{
		error: err,
	}
}

type userfriendly struct {
	error
}

// user friendly error
func (t userfriendly) UserFriendly() {}
func (t userfriendly) Unwrap() error {
	return t.error
}
func (t userfriendly) Cause() error {
	return t.error
}

func MaybeLog(err error) {
	if err == nil {
		return
	}

	if cause := log.Output(1, fmt.Sprintln(err)); cause != nil {
		log.Println(cause)
	}
}
