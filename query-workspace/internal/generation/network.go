package generation

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"
	"time"

	"tpt-graph/query-workspace/internal/workspace"
)

type networkKey struct{}

type networkDiagnostics struct {
	mu       sync.Mutex
	attempts []*workspace.HTTPAttempt
}

func (n *networkDiagnostics) snapshot() []workspace.HTTPAttempt {
	n.mu.Lock()
	defer n.mu.Unlock()
	attempts := make([]workspace.HTTPAttempt, len(n.attempts))
	for i, attempt := range n.attempts {
		attempts[i] = *attempt
	}
	return attempts
}

type diagnosticTransport struct {
	base http.RoundTripper
}

func (t diagnosticTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	diagnostics, ok := request.Context().Value(networkKey{}).(*networkDiagnostics)
	if !ok {
		return t.base.RoundTrip(request)
	}
	started := time.Now()
	attempt := &workspace.HTTPAttempt{}
	diagnostics.mu.Lock()
	diagnostics.attempts = append(diagnostics.attempts, attempt)
	diagnostics.mu.Unlock()
	var dnsStarted, tlsStarted, written time.Time
	connects := make(map[string]time.Time)
	ms := func(duration time.Duration) *float64 {
		value := float64(duration) / float64(time.Millisecond)
		return &value
	}
	clientTrace := &httptrace.ClientTrace{
		DNSStart: func(httptrace.DNSStartInfo) {
			diagnostics.mu.Lock()
			defer diagnostics.mu.Unlock()
			dnsStarted = time.Now()
		},
		DNSDone: func(httptrace.DNSDoneInfo) {
			diagnostics.mu.Lock()
			defer diagnostics.mu.Unlock()
			if !dnsStarted.IsZero() {
				attempt.DNSMS = ms(time.Since(dnsStarted))
			}
		},
		ConnectStart: func(network, address string) {
			diagnostics.mu.Lock()
			defer diagnostics.mu.Unlock()
			connects[network+"\x00"+address] = time.Now()
		},
		ConnectDone: func(network, address string, _ error) {
			diagnostics.mu.Lock()
			defer diagnostics.mu.Unlock()
			if start, ok := connects[network+"\x00"+address]; ok {
				attempt.ConnectMS = ms(time.Since(start))
			}
		},
		TLSHandshakeStart: func() {
			diagnostics.mu.Lock()
			defer diagnostics.mu.Unlock()
			tlsStarted = time.Now()
		},
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			diagnostics.mu.Lock()
			defer diagnostics.mu.Unlock()
			if !tlsStarted.IsZero() {
				attempt.TLSMS = ms(time.Since(tlsStarted))
			}
		},
		GotConn: func(info httptrace.GotConnInfo) {
			diagnostics.mu.Lock()
			defer diagnostics.mu.Unlock()
			reused := info.Reused
			attempt.ConnectionReused = &reused
			attempt.ConnectionReadyMS = ms(time.Since(started))
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			diagnostics.mu.Lock()
			defer diagnostics.mu.Unlock()
			if info.Err == nil {
				written = time.Now()
				attempt.RequestWriteMS = ms(written.Sub(started))
			}
		},
		GotFirstResponseByte: func() {
			diagnostics.mu.Lock()
			defer diagnostics.mu.Unlock()
			now := time.Now()
			attempt.TimeToFirstByteMS = ms(now.Sub(started))
			if !written.IsZero() {
				attempt.WaitAfterWriteMS = ms(now.Sub(written))
			}
		},
	}
	request = request.Clone(httptrace.WithClientTrace(request.Context(), clientTrace))
	response, err := t.base.RoundTrip(request)
	finish := func(err error) {
		diagnostics.mu.Lock()
		defer diagnostics.mu.Unlock()
		attempt.DurationMS = float64(time.Since(started)) / float64(time.Millisecond)
		if err != nil && !errors.Is(err, io.EOF) {
			attempt.ErrorType = fmt.Sprintf("%T", err)
		}
	}
	if err != nil {
		finish(err)
		return response, err
	}
	diagnostics.mu.Lock()
	attempt.HTTPStatus = response.StatusCode
	attempt.RequestID = response.Header.Get("X-Goog-Request-Id")
	if attempt.RequestID == "" {
		attempt.RequestID = response.Header.Get("X-Request-Id")
	}
	diagnostics.mu.Unlock()
	response.Body = &diagnosticBody{ReadCloser: response.Body, finish: finish}
	return response, nil
}

type diagnosticBody struct {
	io.ReadCloser
	once   sync.Once
	finish func(error)
}

func (b *diagnosticBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	if err != nil {
		b.once.Do(func() { b.finish(err) })
	}
	return n, err
}

func (b *diagnosticBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.finish(err) })
	return err
}

func withNetworkDiagnostics(ctx context.Context, diagnostics *networkDiagnostics) context.Context {
	return context.WithValue(ctx, networkKey{}, diagnostics)
}
