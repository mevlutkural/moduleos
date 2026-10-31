package containerinit

import (
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/config"
)

func TestHealthcheckDefaultsMatchConfigDefaults(t *testing.T) {
	configType := reflect.TypeOf(config.Config{})
	for field, want := range map[string]string{"ListenAddress": defaultListenAddress, "Port": defaultPort} {
		structField, found := configType.FieldByName(field)
		if !found {
			t.Fatalf("config.Config has no %s field", field)
		}
		if got := structField.Tag.Get("envDefault"); got != want {
			t.Fatalf("config %s default = %q, healthcheck default = %q", field, got, want)
		}
	}
}

func TestHealthcheckHostMapping(t *testing.T) {
	tests := []struct {
		name    string
		value   *string
		want    string
		wantErr bool
	}{
		{name: "unset", want: "127.0.0.1"},
		{name: "IPv4 unspecified", value: ptr("0.0.0.0"), want: "127.0.0.1"},
		{name: "IPv6 unspecified", value: ptr("::"), want: "::1"},
		{name: "localhost", value: ptr("localhost"), want: "127.0.0.1"},
		{name: "IPv4 loopback", value: ptr("127.0.0.1"), want: "127.0.0.1"},
		{name: "IPv6 loopback", value: ptr("::1"), want: "::1"},
		{name: "specific IPv4", value: ptr("10.1.2.3"), want: "10.1.2.3"},
		{name: "specific IPv6", value: ptr("fd00::7"), want: "fd00::7"},
		{name: "empty", value: ptr(""), wantErr: true},
		{name: "hostname", value: ptr("moduleos.internal"), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{}
			if test.value != nil {
				values["LISTEN_ADDRESS"] = *test.value
			}
			got, err := healthcheckHost(environmentLookup(values))
			if test.wantErr {
				requireCode(t, err, codeHealthcheckConfigInvalid)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("host = %q, want %q", got, test.want)
			}
		})
	}
}

func TestHealthcheckRejectsInvalidPorts(t *testing.T) {
	for _, port := range []string{"", "0", "65536", "-1", "+0", "http", "3000 "} {
		err := healthcheck(environmentLookup(map[string]string{"PORT": port}), time.Second)
		requireCode(t, err, codeHealthcheckConfigInvalid)
	}
}

func startLivenessServer(t *testing.T, handler http.HandlerFunc) map[string]string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{"LISTEN_ADDRESS": host, "PORT": port}
}

func TestHealthcheckAcceptsPortsThatConfigAccepts(t *testing.T) {
	values := startLivenessServer(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	})
	for _, port := range []string{"0" + values["PORT"], "+" + values["PORT"]} {
		lookup := environmentLookup(map[string]string{"LISTEN_ADDRESS": values["LISTEN_ADDRESS"], "PORT": port})
		if err := healthcheck(lookup, time.Second); err != nil {
			t.Fatalf("PORT=%q: healthcheck() error = %v", port, err)
		}
	}
}

func TestHealthcheckProbesOnlyLiveness(t *testing.T) {
	var requests atomic.Int32
	values := startLivenessServer(t, func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/api/v1/live" {
			t.Errorf("request = %s %s, want GET /api/v1/live", request.Method, request.URL.Path)
		}
		response.WriteHeader(http.StatusOK)
	})

	if err := healthcheck(environmentLookup(values), time.Second); err != nil {
		t.Fatalf("healthcheck() error = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestHealthcheckTreatsNonOKAsUnhealthy(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusServiceUnavailable, http.StatusUnauthorized, http.StatusInternalServerError} {
		values := startLivenessServer(t, func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(status)
		})
		requireCode(t, healthcheck(environmentLookup(values), time.Second), codeHealthcheckUnhealthy)
	}
}

func TestHealthcheckDoesNotFollowRedirects(t *testing.T) {
	var redirected atomic.Bool
	values := startLivenessServer(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/elsewhere" {
			redirected.Store(true)
			response.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(response, request, "/elsewhere", http.StatusFound)
	})

	requireCode(t, healthcheck(environmentLookup(values), time.Second), codeHealthcheckUnhealthy)
	if redirected.Load() {
		t.Fatal("healthcheck followed a redirect")
	}
}

func TestHealthcheckIgnoresProxyEnvironment(t *testing.T) {
	var proxied atomic.Bool
	proxy := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		proxied.Store(true)
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(proxy.Close)
	for _, key := range []string{"HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		t.Setenv(key, proxy.URL)
	}
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	values := startLivenessServer(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	})

	if err := healthcheck(environmentLookup(values), time.Second); err != nil {
		t.Fatalf("healthcheck() error = %v", err)
	}
	if proxied.Load() {
		t.Fatal("healthcheck used the proxy from the environment")
	}
}

func TestHealthcheckBoundsTimeAndBody(t *testing.T) {
	release := make(chan struct{})
	slow := startLivenessServer(t, func(response http.ResponseWriter, _ *http.Request) {
		<-release
		response.WriteHeader(http.StatusOK)
	})
	t.Cleanup(func() { close(release) })
	started := time.Now()
	requireCode(t, healthcheck(environmentLookup(slow), 50*time.Millisecond), codeHealthcheckUnreachable)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("healthcheck took %s, want bounded by its timeout", elapsed)
	}

	large := startLivenessServer(t, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		chunk := []byte(strings.Repeat("x", 64*1024))
		for range 64 {
			if _, err := response.Write(chunk); err != nil {
				return
			}
		}
	})
	if err := healthcheck(environmentLookup(large), 5*time.Second); err != nil {
		t.Fatalf("healthcheck() with a large body error = %v", err)
	}
}

func TestHealthcheckReportsUnreachableDaemon(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	err = healthcheck(environmentLookup(map[string]string{"LISTEN_ADDRESS": host, "PORT": port}), time.Second)

	requireCode(t, err, codeHealthcheckUnreachable)
}
