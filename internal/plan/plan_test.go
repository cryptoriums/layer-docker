package plan

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(body string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		fmt.Fprint(w, body)
	}))
}

func TestCurrentReturnsScheduledPlan(t *testing.T) {
	srv := serve(`{"plan":{"name":"v6.1.8","time":"0001-01-01T00:00:00Z","height":"22460000","info":""}}`, http.StatusOK)
	defer srv.Close()

	p, err := New([]string{srv.URL}, 0).Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if p == nil {
		t.Fatal("expected a plan")
	}
	if p.Name != "v6.1.8" || p.Height != 22460000 {
		t.Fatalf("plan = %+v, want v6.1.8 @ 22460000", p)
	}
}

// No scheduled upgrade is the normal state and must not read as an error.
func TestCurrentReturnsNilWhenNoPlan(t *testing.T) {
	srv := serve(`{"plan":null}`, http.StatusOK)
	defer srv.Close()

	p, err := New([]string{srv.URL}, 0).Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if p != nil {
		t.Fatalf("plan = %+v, want nil when nothing is scheduled", p)
	}
}

// Our own node being down is exactly when an upgrade is most likely to be missed,
// so a dead first endpoint must fall through to the next.
func TestCurrentFailsOverToNextEndpoint(t *testing.T) {
	dead := serve("", http.StatusInternalServerError)
	defer dead.Close()
	live := serve(`{"plan":{"name":"v6.1.9","height":"23000000"}}`, http.StatusOK)
	defer live.Close()

	p, err := New([]string{dead.URL, live.URL}, 0).Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if p == nil || p.Name != "v6.1.9" {
		t.Fatalf("plan = %+v, want v6.1.9 from the fallback endpoint", p)
	}
}

func TestCurrentErrorsWhenAllEndpointsFail(t *testing.T) {
	dead := serve("", http.StatusBadGateway)
	defer dead.Close()

	if _, err := New([]string{dead.URL}, 0).Current(context.Background()); err == nil {
		t.Fatal("expected an error when every endpoint fails")
	}
}

// A malformed height must be an error, not height 0 - staging against height 0
// would look like an upgrade that already passed.
func TestCurrentRejectsUnparseableHeight(t *testing.T) {
	srv := serve(`{"plan":{"name":"v6.1.8","height":"not-a-number"}}`, http.StatusOK)
	defer srv.Close()

	if _, err := New([]string{srv.URL}, 0).Current(context.Background()); err == nil {
		t.Fatal("expected an error for an unparseable height")
	}
}
