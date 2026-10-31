package containerinit

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

const (
	// defaultListenAddress and defaultPort mirror the config envDefault tags;
	// a test keeps them aligned.
	defaultListenAddress   = "127.0.0.1"
	defaultPort            = "3000"
	healthcheckBodyLimit   = 4096
	healthcheckLivenessURL = "/api/v1/live"
)

// runHealthcheck probes liveness as the fixed daemon identity. Docker runs a
// HEALTHCHECK as the image user, which is root so that start can prepare, so
// a root probe first drops to the runtime identity irreversibly.
func runHealthcheck(deps dependencies) error {
	sys, err := deps.newSystem()
	if err != nil {
		return err
	}
	current, err := sys.identity()
	if err != nil {
		return fail(codePrivilegeVerifyFailed)
	}
	if current.effectiveUID == 0 {
		if err := dropPrivileges(sys, []int{RuntimeGID}); err != nil {
			return err
		}
	} else if current.includesRoot() {
		return fail(codeRootIdentityRefused)
	}
	return healthcheck(deps.lookup, deps.healthcheckTimeout)
}

// healthcheck probes process liveness only. It never calls /ready, so a
// Docker outage cannot make the container unhealthy and trigger a restart.
func healthcheck(lookup func(string) (string, bool), timeout time.Duration) error {
	host, err := healthcheckHost(lookup)
	if err != nil {
		return err
	}
	value, set := lookup("PORT")
	if !set {
		value = defaultPort
	}
	// Mirror config validation, which accepts any strconv.Atoi value in range.
	number, err := strconv.Atoi(value)
	if err != nil || number < 1 || number > 65535 {
		return fail(codeHealthcheckConfigInvalid)
	}
	port := strconv.Itoa(number)

	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Get("http://" + net.JoinHostPort(host, port) + healthcheckLivenessURL)
	if err != nil {
		return fail(codeHealthcheckUnreachable)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, healthcheckBodyLimit))
	if response.StatusCode != http.StatusOK {
		return fail(codeHealthcheckUnhealthy)
	}
	return nil
}

func healthcheckHost(lookup func(string) (string, bool)) (string, error) {
	value, set := lookup("LISTEN_ADDRESS")
	if !set {
		value = defaultListenAddress
	}
	if value == "localhost" {
		return "127.0.0.1", nil
	}
	address := net.ParseIP(value)
	if address == nil {
		return "", fail(codeHealthcheckConfigInvalid)
	}
	if address.IsUnspecified() {
		if address.To4() != nil {
			return "127.0.0.1", nil
		}
		return "::1", nil
	}
	return address.String(), nil
}
