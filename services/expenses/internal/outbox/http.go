package outbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/npavlov/expense-platform/libs/platform/httpx"
	"github.com/npavlov/expense-platform/libs/platform/sigauth"
)

// HTTPDeliverer posts events to the audit service's internal endpoint.
type HTTPDeliverer struct {
	URL    string
	Signer sigauth.Signer
	Client *http.Client
}

// NewHTTPDeliverer returns a deliverer for a fixed, configured URL. Redirects
// are not followed: the destination never comes from data.
func NewHTTPDeliverer(url string, signer sigauth.Signer) *HTTPDeliverer {
	return &HTTPDeliverer{
		URL:    url,
		Signer: signer,
		Client: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// Deliver sends the payload exactly as stored, signed over those bytes.
// 2xx is success; 400-499 except 408 and 429 is permanent; anything else,
// including timeouts and connection errors, is transient.
func (d *HTTPDeliverer) Deliver(ctx context.Context, e Event) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.URL, bytes.NewReader(e.Payload))
	if err != nil {
		return &PermanentError{Reason: "build request: " + err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(sigauth.Header, d.Signer.Sign(e.Payload))
	if e.RequestID != "" {
		req.Header.Set(httpx.RequestIDHeader, e.RequestID)
	}

	resp, err := d.Client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return errors.New("timeout")
		}
		return fmt.Errorf("transport: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("receiver busy: HTTP %d", resp.StatusCode)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return &PermanentError{Reason: fmt.Sprintf("receiver rejected event: HTTP %d", resp.StatusCode)}
	default:
		return fmt.Errorf("receiver error: HTTP %d", resp.StatusCode)
	}
}
