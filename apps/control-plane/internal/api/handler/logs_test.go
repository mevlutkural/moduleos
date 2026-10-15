package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type logServiceStub struct {
	mu      sync.Mutex
	request swarm.LogRequest
	name    string
	stream  *swarm.LogStream
	err     error
	wait    bool
}

type logServiceFunc func(context.Context, string, swarm.LogRequest) (*swarm.LogStream, error)

func (f logServiceFunc) OpenApplicationLogs(ctx context.Context, name string, request swarm.LogRequest) (*swarm.LogStream, error) {
	return f(ctx, name, request)
}

type timeoutMalformedReader struct {
	ctx context.Context
}

func (r *timeoutMalformedReader) Read(payload []byte) (int, error) {
	<-r.ctx.Done()
	payload[0] = 1
	return 1, io.EOF
}

func (*timeoutMalformedReader) Close() error { return nil }

type timeoutEOFReader struct {
	ctx context.Context
}

func (r *timeoutEOFReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, io.EOF
}

func (*timeoutEOFReader) Close() error { return nil }

func (s *logServiceStub) OpenApplicationLogs(ctx context.Context, name string, request swarm.LogRequest) (*swarm.LogStream, error) {
	s.mu.Lock()
	s.name, s.request = name, request
	s.mu.Unlock()
	if s.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.stream, s.err
}

type trackingReader struct {
	io.Reader
	mu     sync.Mutex
	closed int
}

type contextBoundLogService struct {
	reader  *trackingReader
	ctxDone <-chan struct{}
	stop    func() bool
}

func (s *contextBoundLogService) OpenApplicationLogs(ctx context.Context, _ string, _ swarm.LogRequest) (*swarm.LogStream, error) {
	s.ctxDone = ctx.Done()
	s.stop = context.AfterFunc(ctx, func() { _ = s.reader.Close() })
	return &swarm.LogStream{Reader: &contextBoundLogReader{service: s}, Framing: swarm.LogFramingMultiplexed}, nil
}

type contextBoundLogReader struct {
	service *contextBoundLogService
	once    sync.Once
}

func (r *contextBoundLogReader) Read(payload []byte) (int, error) {
	return r.service.reader.Read(payload)
}

func (r *contextBoundLogReader) Close() error {
	r.once.Do(func() {
		if r.service.stop != nil {
			r.service.stop()
		}
		_ = r.service.reader.Close()
	})
	return nil
}

type panicLogReader struct{}

func (panicLogReader) Read([]byte) (int, error) { panic("private runtime panic") }
func (panicLogReader) Close() error             { return nil }

func (r *trackingReader) Close() error {
	r.mu.Lock()
	r.closed++
	r.mu.Unlock()
	return nil
}

func (r *trackingReader) closeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

func validLogOptions() LogOptions {
	return LogOptions{MaxTail: 1000, MaxPayloadBytes: 1024, MaxConnections: 2, MaxConnectionsPerApp: 1, InitialTail: 50, TailTimeout: time.Second, OpenTimeout: time.Second, HeartbeatInterval: 10 * time.Millisecond, IdleTimeout: 50 * time.Millisecond, MaxDuration: time.Second}
}

func newTestLogsHandler(t *testing.T, service LogService, mutate ...func(*LogOptions)) *LogsHandler {
	t.Helper()
	options := validLogOptions()
	for _, change := range mutate {
		change(&options)
	}
	handler, err := NewLogsHandler(service, slog.New(slog.NewTextHandler(io.Discard, nil)), options)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestNewLogsHandlerRejectsInvalidDependenciesAndOptions(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewLogsHandler(nil, logger, validLogOptions()); !errors.Is(err, ErrMissingLogService) {
		t.Fatalf("error = %v", err)
	}
	if _, err := NewLogsHandler(&logServiceStub{}, nil, validLogOptions()); !errors.Is(err, ErrMissingLogLogger) {
		t.Fatalf("error = %v", err)
	}
	tests := []func(*LogOptions){
		func(o *LogOptions) { o.MaxTail = 99 }, func(o *LogOptions) { o.MaxPayloadBytes = 1 },
		func(o *LogOptions) { o.MaxConnections = 0 }, func(o *LogOptions) { o.MaxConnectionsPerApp = 3 },
		func(o *LogOptions) { o.InitialTail = 0 }, func(o *LogOptions) { o.TailTimeout = 0 },
		func(o *LogOptions) { o.OpenTimeout = 0 }, func(o *LogOptions) { o.HeartbeatInterval = o.IdleTimeout },
		func(o *LogOptions) { o.IdleTimeout = o.MaxDuration },
	}
	for index, mutate := range tests {
		options := validLogOptions()
		mutate(&options)
		if _, err := NewLogsHandler(&logServiceStub{}, logger, options); !errors.Is(err, ErrInvalidLogOptions) {
			t.Fatalf("case %d error = %v", index, err)
		}
	}
}

func TestLogsTailReturnsStructuredOutputAndClosesReader(t *testing.T) {
	reader := &trackingReader{Reader: bytes.NewReader(append(dockerFrame(1, "ready\n"), dockerFrame(2, "warning\n")...))}
	service := &logServiceStub{stream: &swarm.LogStream{Reader: reader, Framing: swarm.LogFramingMultiplexed}}
	handler := newTestLogsHandler(t, service)
	app := fiber.New()
	app.Use(func(c fiber.Ctx) error { apiresponse.EnsureRequestID(c); return c.Next() })
	app.Get("/apps/:name/logs", handler.Tail)
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/apps/api/logs?tail=2", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body LogTailResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || body.Total != 2 || body.Logs[0].Stream != "stdout" || body.Logs[1].Stream != "stderr" {
		t.Fatalf("status/body = %d/%#v", response.StatusCode, body)
	}
	if reader.closeCount() != 1 || service.request != (swarm.LogRequest{Tail: 2}) || service.name != "api" {
		t.Fatalf("close/request/name = %d/%#v/%q", reader.closeCount(), service.request, service.name)
	}
}

func TestLogsInputsDetachApplicationNameFromRequestStorage(t *testing.T) {
	handler := newTestLogsHandler(t, &logServiceStub{})
	app := fiber.New()
	app.Get("/apps/:name/logs", func(c fiber.Ctx) error {
		parameter := c.Params("name")
		name, _, err := handler.inputs(c, defaultLogTail, false)
		if err != nil {
			return err
		}
		if unsafe.StringData(name) == unsafe.StringData(parameter) {
			t.Fatal("application name still aliases Fiber request storage")
		}
		return c.SendStatus(fiber.StatusNoContent)
	})
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/apps/api/logs", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestLogsTailValidatesInputAndMapsFailures(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		body   io.Reader
		err    error
		wait   bool
		status int
		code   string
	}{
		{name: "bad name", path: "/apps/Bad/logs", status: 400, code: "invalid_request"},
		{name: "unknown query", path: "/apps/api/logs?since=1", status: 422, code: "validation_failed"},
		{name: "duplicate tail", path: "/apps/api/logs?tail=1&tail=2", status: 422, code: "validation_failed"},
		{name: "noncanonical tail", path: "/apps/api/logs?tail=01", status: 422, code: "validation_failed"},
		{name: "tail too large", path: "/apps/api/logs?tail=1001", status: 422, code: "validation_failed"},
		{name: "body", path: "/apps/api/logs", body: strings.NewReader("x"), status: 400, code: "invalid_request"},
		{name: "missing", path: "/apps/api/logs", err: store.ErrNotFound, status: 404, code: "application_not_found"},
		{name: "runtime", path: "/apps/api/logs", err: errors.New("docker down"), status: 503, code: "runtime_logs_unavailable"},
		{name: "timeout", path: "/apps/api/logs", wait: true, status: 504, code: "runtime_timeout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &logServiceStub{err: test.err, wait: test.wait, stream: &swarm.LogStream{Reader: io.NopCloser(strings.NewReader("")), Framing: swarm.LogFramingMultiplexed}}
			handler := newTestLogsHandler(t, service, func(options *LogOptions) { options.TailTimeout = 10 * time.Millisecond })
			app := fiber.New()
			app.Get("/apps/:name/logs", handler.Tail)
			response, err := app.Test(httptest.NewRequest(http.MethodGet, test.path, test.body), fiber.TestConfig{Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, test.status)
			}
			if test.code != "" {
				encoded, readErr := io.ReadAll(response.Body)
				var errorBody apiresponse.ErrorResponse
				decodeErr := json.Unmarshal(encoded, &errorBody)
				if readErr != nil || decodeErr != nil || errorBody.Error.Code != test.code {
					t.Fatalf("body/read/decode = %s/%v/%v", encoded, readErr, decodeErr)
				}
			}
		})
	}
}

func TestLogsTailClosesReaderReturnedWithOpenError(t *testing.T) {
	reader := &trackingReader{Reader: strings.NewReader("")}
	handler := newTestLogsHandler(t, &logServiceStub{
		stream: &swarm.LogStream{Reader: reader, Framing: swarm.LogFramingMultiplexed},
		err:    errors.New("open failed"),
	})
	app := fiber.New()
	app.Get("/apps/:name/logs", handler.Tail)
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/apps/api/logs", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusServiceUnavailable || reader.closeCount() != 1 {
		t.Fatalf("status/close count = %d/%d", response.StatusCode, reader.closeCount())
	}
}

func TestLogsTailAlwaysClassifiesReadDeadlineAsTimeout(t *testing.T) {
	for _, test := range []struct {
		name   string
		reader func(context.Context) io.ReadCloser
	}{
		{name: "malformed result", reader: func(ctx context.Context) io.ReadCloser { return &timeoutMalformedReader{ctx: ctx} }},
		{name: "clean EOF result", reader: func(ctx context.Context) io.ReadCloser { return &timeoutEOFReader{ctx: ctx} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := logServiceFunc(func(ctx context.Context, _ string, _ swarm.LogRequest) (*swarm.LogStream, error) {
				return &swarm.LogStream{Reader: test.reader(ctx), Framing: swarm.LogFramingMultiplexed}, nil
			})
			handler := newTestLogsHandler(t, service, func(options *LogOptions) { options.TailTimeout = time.Millisecond })
			app := fiber.New()
			app.Get("/apps/:name/logs", handler.Tail)
			for iteration := 0; iteration < 50; iteration++ {
				response, err := app.Test(httptest.NewRequest(http.MethodGet, "/apps/api/logs", nil), fiber.TestConfig{Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				var body apiresponse.ErrorResponse
				decodeErr := json.NewDecoder(response.Body).Decode(&body)
				_ = response.Body.Close()
				if decodeErr != nil || response.StatusCode != fiber.StatusGatewayTimeout || body.Error.Code != "runtime_timeout" {
					t.Fatalf("iteration/status/body/error = %d/%d/%#v/%v", iteration, response.StatusCode, body, decodeErr)
				}
			}
		})
	}
}

func TestLogsStreamWriterEmitsEventsHeartbeatAndTerminalError(t *testing.T) {
	handler := newTestLogsHandler(t, &logServiceStub{})
	data := append(dockerFrame(1, "ready\n"), dockerFrame(3, "private runtime failure")...)
	reader := &ownedLogReader{reader: io.NopCloser(bytes.NewReader(data))}
	var output bytes.Buffer
	reason := handler.writeStream(bufio.NewWriter(&output), context.Background(), reader, swarm.LogFramingMultiplexed, "request-id")
	if reason != "upstream_error" || !strings.Contains(output.String(), "event: log") || !strings.Contains(output.String(), "event: error") || !strings.Contains(output.String(), "log_stream_unavailable") || strings.Contains(output.String(), "private runtime failure") {
		t.Fatalf("reason/output = %q/%q", reason, output.String())
	}
}

func TestLogsTailMapsDockerSystemErrorToSafeRetryableFailure(t *testing.T) {
	reader := &trackingReader{Reader: bytes.NewReader(dockerFrame(3, "private runtime failure\n"))}
	service := &logServiceStub{stream: &swarm.LogStream{Reader: reader, Framing: swarm.LogFramingMultiplexed}}
	handler := newTestLogsHandler(t, service)
	app := fiber.New()
	app.Get("/apps/:name/logs", handler.Tail)
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/apps/api/logs", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusServiceUnavailable || !bytes.Contains(body, []byte(`"code":"runtime_logs_unavailable"`)) || bytes.Contains(body, []byte("private runtime failure")) {
		t.Fatalf("status/body = %d/%s", response.StatusCode, body)
	}
	if reader.closeCount() != 1 {
		t.Fatalf("reader close count = %d", reader.closeCount())
	}
}

func TestLogsStreamContainsReaderPanicAndReturnsSafeError(t *testing.T) {
	handler := newTestLogsHandler(t, &logServiceStub{})
	var output bytes.Buffer
	reason := handler.writeStream(bufio.NewWriter(&output), context.Background(), &ownedLogReader{reader: panicLogReader{}}, swarm.LogFramingMultiplexed, "request-id")
	if reason != "upstream_error" || !strings.Contains(output.String(), "log_stream_unavailable") || strings.Contains(output.String(), "private runtime panic") {
		t.Fatalf("reason/output = %q/%q", reason, output.String())
	}
}

func TestSSELogEncodingCannotInjectFieldsAndNormalizesInvalidUTF8(t *testing.T) {
	var output bytes.Buffer
	writer := bufio.NewWriter(&output)
	message := "line\ndata: injected\r" + string([]byte{0xff})
	if err := writeSSELog(writer, LogEntryResponse{Stream: "stdout", Message: message}); err != nil {
		t.Fatal(err)
	}
	encoded := output.String()
	if strings.Count(encoded, "\ndata:") != 1 || strings.Contains(encoded, "\ndata: injected") || !strings.Contains(encoded, `\ufffd`) {
		t.Fatalf("unsafe SSE encoding: %q", encoded)
	}
}

func TestLogsStreamWriterHonorsIdleAndMaximumDuration(t *testing.T) {
	for _, test := range []struct {
		name       string
		idle       time.Duration
		maximum    time.Duration
		deadline   time.Duration
		wantReason string
	}{
		{name: "idle", idle: 20 * time.Millisecond, maximum: time.Second, deadline: time.Second, wantReason: "idle_timeout"},
		{name: "maximum", idle: 40 * time.Millisecond, maximum: time.Second, deadline: 25 * time.Millisecond, wantReason: "max_duration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pipeReader, pipeWriter := io.Pipe()
			handler := newTestLogsHandler(t, &logServiceStub{}, func(options *LogOptions) {
				options.HeartbeatInterval = 5 * time.Millisecond
				options.IdleTimeout = test.idle
				options.MaxDuration = test.maximum
			})
			ctx, cancel := context.WithTimeout(context.Background(), test.deadline)
			defer cancel()
			var output bytes.Buffer
			reason := handler.writeStream(bufio.NewWriter(&output), ctx, &ownedLogReader{reader: pipeReader}, swarm.LogFramingMultiplexed, "request-id")
			_ = pipeWriter.Close()
			if reason != test.wantReason || !strings.Contains(output.String(), ": keepalive") {
				t.Fatalf("reason/output = %q/%q", reason, output.String())
			}
		})
	}
}

func TestLogsHandlerCancelsAndRejectsStreamsDuringShutdown(t *testing.T) {
	handler := newTestLogsHandler(t, &logServiceStub{})
	ctx, cancel := context.WithCancel(context.Background())
	id, ok := handler.registerStream(cancel)
	if !ok {
		t.Fatal("registration rejected")
	}
	handler.CloseActiveStreams()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("active stream was not cancelled")
	}
	handler.unregisterStream(id)
	if _, ok := handler.registerStream(func() {}); ok {
		t.Fatal("stream registered after shutdown")
	}
}

func TestLogsStreamReturnsSSEAndReleasesCapacity(t *testing.T) {
	service := &logServiceStub{stream: &swarm.LogStream{Reader: io.NopCloser(bytes.NewReader(dockerFrame(1, "ready\n"))), Framing: swarm.LogFramingMultiplexed}}
	handler := newTestLogsHandler(t, service)
	app := fiber.New()
	app.Get("/apps/:name/logs/stream", handler.Stream)
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/apps/api/logs/stream?tail=7", nil), fiber.TestConfig{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get(fiber.HeaderContentType), "text/event-stream") || !strings.Contains(string(body), `"stream":"stdout"`) {
		t.Fatalf("status/headers/body = %d/%v/%q", response.StatusCode, response.Header, body)
	}
	if !service.request.Follow || service.request.Tail != 7 {
		t.Fatalf("request = %#v", service.request)
	}
	active, apps := handler.limiter.snapshot()
	if active != 0 || apps != 0 {
		t.Fatalf("capacity leaked: %d/%d", active, apps)
	}
}

func TestLogsStreamRejectsReplayAndTimesOutOpening(t *testing.T) {
	t.Run("replay", func(t *testing.T) {
		handler := newTestLogsHandler(t, &logServiceStub{})
		app := fiber.New()
		app.Get("/apps/:name/logs/stream", handler.Stream)
		request := httptest.NewRequest(http.MethodGet, "/apps/api/logs/stream", nil)
		request.Header.Set("Last-Event-ID", "1")
		response, err := app.Test(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != fiber.StatusUnprocessableEntity {
			t.Fatalf("status = %d", response.StatusCode)
		}
	})
	t.Run("open timeout", func(t *testing.T) {
		handler := newTestLogsHandler(t, &logServiceStub{wait: true}, func(options *LogOptions) { options.OpenTimeout = 10 * time.Millisecond })
		app := fiber.New()
		app.Get("/apps/:name/logs/stream", handler.Stream)
		response, err := app.Test(httptest.NewRequest(http.MethodGet, "/apps/api/logs/stream", nil), fiber.TestConfig{Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != fiber.StatusGatewayTimeout {
			t.Fatalf("status = %d", response.StatusCode)
		}
		active, apps := handler.limiter.snapshot()
		if active != 0 || apps != 0 {
			t.Fatalf("capacity leaked: %d/%d", active, apps)
		}
	})
}

func TestOpenStreamKeepsSuccessfulContextAliveUntilReaderCloses(t *testing.T) {
	service := &contextBoundLogService{reader: &trackingReader{Reader: strings.NewReader("ready\n")}}
	handler := newTestLogsHandler(t, service, func(options *LogOptions) { options.OpenTimeout = 20 * time.Millisecond })
	stream, err := handler.openStream(context.Background(), "api", swarm.LogRequest{Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.ctxDone:
		t.Fatal("successful open context was cancelled before the stream closed")
	case <-time.After(2 * handler.options.OpenTimeout):
	}
	if service.reader.closeCount() != 0 {
		t.Fatalf("reader close count before ownership release = %d", service.reader.closeCount())
	}
	if err := stream.Reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-service.ctxDone:
	case <-time.After(time.Second):
		t.Fatal("stream close did not cancel the owned open context")
	}
	if service.reader.closeCount() != 1 {
		t.Fatalf("reader close count = %d", service.reader.closeCount())
	}
}

func TestOpenStreamCleansUpFailedAndCancelledOpens(t *testing.T) {
	t.Run("service error closes returned reader", func(t *testing.T) {
		reader := &trackingReader{Reader: strings.NewReader("")}
		handler := newTestLogsHandler(t, &logServiceStub{
			stream: &swarm.LogStream{Reader: reader, Framing: swarm.LogFramingMultiplexed},
			err:    errors.New("open failed"),
		})
		stream, err := handler.openStream(context.Background(), "api", swarm.LogRequest{Follow: true})
		if err == nil || stream == nil || reader.closeCount() != 1 {
			t.Fatalf("stream/error/close count = %#v/%v/%d", stream, err, reader.closeCount())
		}
	})

	t.Run("parent cancellation wins", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		handler := newTestLogsHandler(t, &logServiceStub{wait: true})
		if stream, err := handler.openStream(ctx, "api", swarm.LogRequest{Follow: true}); stream != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("stream/error = %#v/%v", stream, err)
		}
	})

	t.Run("completed result cannot bypass parent cancellation", func(t *testing.T) {
		for iteration := 0; iteration < 100; iteration++ {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			reader := &trackingReader{Reader: strings.NewReader("ready\n")}
			handler := newTestLogsHandler(t, &logServiceStub{stream: &swarm.LogStream{Reader: reader, Framing: swarm.LogFramingMultiplexed}})
			stream, err := handler.openStream(ctx, "api", swarm.LogRequest{Follow: true})
			if stream != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("iteration/stream/error = %d/%#v/%v", iteration, stream, err)
			}
			deadline := time.Now().Add(time.Second)
			for reader.closeCount() == 0 && time.Now().Before(deadline) {
				runtime.Gosched()
			}
			if reader.closeCount() != 1 {
				t.Fatalf("iteration %d reader close count = %d", iteration, reader.closeCount())
			}
		}
	})
}

func TestValidOwnedLogReaderRejectsInvalidRuntimeStateAndClosesReader(t *testing.T) {
	if reader, err := validOwnedLogReader(nil); reader != nil || !errors.Is(err, errInvalidLogState) {
		t.Fatalf("reader/error = %#v/%v", reader, err)
	}
	tracking := &trackingReader{Reader: strings.NewReader("")}
	if reader, err := validOwnedLogReader(&swarm.LogStream{Reader: tracking, Framing: "unknown"}); reader != nil || !errors.Is(err, errInvalidLogState) {
		t.Fatalf("reader/error = %#v/%v", reader, err)
	}
	if tracking.closeCount() != 1 {
		t.Fatalf("reader close count = %d", tracking.closeCount())
	}
	raw := &trackingReader{Reader: strings.NewReader("private daemon error")}
	if reader, err := validOwnedLogReader(&swarm.LogStream{Reader: raw, Framing: "raw"}); reader != nil || !errors.Is(err, errInvalidLogState) {
		t.Fatalf("raw reader/error = %#v/%v", reader, err)
	}
	if raw.closeCount() != 1 {
		t.Fatalf("raw reader close count = %d", raw.closeCount())
	}
}

func TestLogsStreamReturnsRetryableCapacityErrorBeforeOpeningRuntime(t *testing.T) {
	service := &logServiceStub{}
	handler := newTestLogsHandler(t, service)
	lease, ok := handler.limiter.acquire("api")
	if !ok {
		t.Fatal("failed to reserve application capacity")
	}
	defer lease.Release()
	app := fiber.New()
	app.Get("/apps/:name/logs/stream", handler.Stream)
	response, err := app.Test(httptest.NewRequest(http.MethodGet, "/apps/api/logs/stream", nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body apiresponse.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != fiber.StatusTooManyRequests || response.Header.Get(fiber.HeaderRetryAfter) != "1" || body.Error.Code != "log_stream_limit" || !body.Error.Retryable {
		t.Fatalf("status/header/body = %d/%q/%#v", response.StatusCode, response.Header.Get(fiber.HeaderRetryAfter), body)
	}
	if service.name != "" {
		t.Fatalf("runtime was opened for %q", service.name)
	}
}
