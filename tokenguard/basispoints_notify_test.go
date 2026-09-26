package tokenguard

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type notificationTransport func(*http.Request) (*http.Response, error)

func (f notificationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestBasispointsBarkNotificationHTTPFailure(t *testing.T) {
	for _, status := range []int{200, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			c := NewClient(notificationTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "https://api.day.app/push" {
					t.Fatal("unexpected endpoint")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
			}))
			err := c.SendNotification(context.Background(), Config{BarkKey: "synthetic"}, "BPS", "fixed event", true)
			if (err == nil) != (status == 200) || calls != 1 {
				t.Fatal(err, calls)
			}
		})
	}
}
