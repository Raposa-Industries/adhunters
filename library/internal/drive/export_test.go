package drive

import "time"

// NewForTest is New with retries that do not wait.
func NewForTest(app App, refreshToken string) *Client {
	c := New(app, refreshToken)
	c.pause = time.Millisecond
	return c
}
