package generation

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNetworkDiagnosticsCaptureTLSWaitAndConnectionReuse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(15 * time.Millisecond)
		w.Header().Set("X-Goog-Request-Id", "request-123")
		_, _ = w.Write([]byte("response"))
	}))
	defer server.Close()
	client := server.Client()
	client.Transport = diagnosticTransport{base: client.Transport}
	network := &networkDiagnostics{}
	for range 2 {
		request, err := http.NewRequestWithContext(withNetworkDiagnostics(context.Background(), network), http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, readErr := io.Copy(io.Discard, response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("reading response: %v; closing response: %v", readErr, closeErr)
		}
	}
	attempts := network.snapshot()
	if len(attempts) != 2 {
		t.Fatalf("expected separate attempt timings, got %v", attempts)
	}
	first, second := attempts[0], attempts[1]
	if first.TLSMS == nil || first.ConnectMS == nil || first.ConnectionReused == nil || *first.ConnectionReused {
		t.Fatalf("missing first-connection timings: %#v", first)
	}
	if second.ConnectionReused == nil || !*second.ConnectionReused || second.TLSMS != nil || second.ConnectMS != nil {
		t.Fatalf("reused connection reported a new handshake: %#v", second)
	}
	for _, attempt := range attempts {
		if attempt.TimeToFirstByteMS == nil || attempt.WaitAfterWriteMS == nil || *attempt.WaitAfterWriteMS < 10 {
			t.Fatalf("server wait was not measured: %#v", attempt)
		}
		if attempt.DurationMS < *attempt.TimeToFirstByteMS || attempt.HTTPStatus != http.StatusOK || attempt.RequestID != "request-123" {
			t.Fatalf("incorrect completed attempt diagnostics: %#v", attempt)
		}
	}
}
