package handler

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/api/apiresponse"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

const defaultLogTail = 100

var (
	ErrMissingLogService  = errors.New("log service is required")
	ErrMissingLogLogger   = errors.New("log logger is required")
	ErrInvalidLogOptions  = errors.New("invalid log options")
	errInvalidLogState    = errors.New("invalid runtime log state")
	errLogResponseWritten = errors.New("runtime log response already written")
)

type LogService interface {
	OpenApplicationLogs(context.Context, string, swarm.LogRequest) (*swarm.LogStream, error)
}

type LogOptions struct {
	MaxTail              int
	MaxPayloadBytes      int64
	MaxConnections       int
	MaxConnectionsPerApp int
	InitialTail          int
	TailTimeout          time.Duration
	OpenTimeout          time.Duration
	HeartbeatInterval    time.Duration
	IdleTimeout          time.Duration
	MaxDuration          time.Duration
}

type LogsHandler struct {
	service LogService
	log     *slog.Logger
	options LogOptions
	limiter *streamLimiter

	streamsMu     sync.Mutex
	nextStreamID  uint64
	activeStreams map[uint64]context.CancelFunc
	shuttingDown  bool
}

type ownedLogReader struct {
	reader io.ReadCloser
	once   sync.Once
	err    error
}

type cancelOnCloseLogReader struct {
	reader io.ReadCloser
	cancel context.CancelFunc
	once   sync.Once
	err    error
}

type decodeResult struct {
	entry *LogEntryResponse
	err   error
}

type tailDecodeResult struct {
	entries []LogEntryResponse
	err     error
}

func NewLogsHandler(service LogService, log *slog.Logger, options LogOptions) (*LogsHandler, error) {
	if isNilInterface(service) {
		return nil, ErrMissingLogService
	}
	if log == nil {
		return nil, ErrMissingLogLogger
	}
	if err := validateLogOptions(options); err != nil {
		return nil, err
	}
	return &LogsHandler{
		service: service, log: log, options: options,
		limiter:       newStreamLimiter(options.MaxConnections, options.MaxConnectionsPerApp),
		activeStreams: make(map[uint64]context.CancelFunc),
	}, nil
}

func validateLogOptions(options LogOptions) error {
	switch {
	case options.MaxTail < defaultLogTail || options.MaxTail > 10000:
		return fmt.Errorf("%w: max tail must be between %d and 10000", ErrInvalidLogOptions, defaultLogTail)
	case options.MaxPayloadBytes < 1024 || options.MaxPayloadBytes > 16*1024*1024:
		return fmt.Errorf("%w: payload limit is outside supported bounds", ErrInvalidLogOptions)
	case options.MaxConnections < 1 || options.MaxConnections > 1024:
		return fmt.Errorf("%w: connection limit is outside supported bounds", ErrInvalidLogOptions)
	case options.MaxConnectionsPerApp < 1 || options.MaxConnectionsPerApp > options.MaxConnections:
		return fmt.Errorf("%w: per-app connection limit is outside supported bounds", ErrInvalidLogOptions)
	case options.InitialTail < 1 || options.InitialTail > options.MaxTail:
		return fmt.Errorf("%w: initial tail is outside supported bounds", ErrInvalidLogOptions)
	case options.TailTimeout <= 0 || options.TailTimeout > time.Minute || options.OpenTimeout <= 0 || options.OpenTimeout > time.Minute:
		return fmt.Errorf("%w: open and tail timeouts must be positive and at most one minute", ErrInvalidLogOptions)
	case options.HeartbeatInterval <= 0 || options.IdleTimeout <= options.HeartbeatInterval || options.MaxDuration <= options.IdleTimeout:
		return fmt.Errorf("%w: durations must satisfy heartbeat < idle < maximum", ErrInvalidLogOptions)
	default:
		return nil
	}
}

func (h *LogsHandler) Tail(c fiber.Ctx) error {
	name, tail, err := h.inputs(c, defaultLogTail, false)
	if err != nil {
		if errors.Is(err, errLogResponseWritten) {
			return nil
		}
		return err
	}
	ctx, cancel := context.WithTimeout(c.RequestCtx(), h.options.TailTimeout)
	defer cancel()
	stream, err := h.service.OpenApplicationLogs(ctx, name, swarm.LogRequest{Tail: tail})
	if err != nil {
		if stream != nil && stream.Reader != nil {
			_ = stream.Reader.Close()
		}
		return h.writeServiceError(c, "tail_application_logs", name, err)
	}
	owned, err := validOwnedLogReader(stream)
	if err != nil {
		return h.writeServiceError(c, "tail_application_logs_response", name, err)
	}
	result := make(chan tailDecodeResult, 1)
	go func() {
		decoded := tailDecodeResult{}
		defer func() {
			if recover() != nil {
				decoded = tailDecodeResult{err: errInvalidLogState}
			}
			result <- decoded
		}()
		decoded.entries, decoded.err = decodeTail(owned.reader, stream.Framing, h.options.MaxPayloadBytes, tail)
	}()

	var entries []LogEntryResponse
	select {
	case decoded := <-result:
		if ctx.Err() != nil {
			_ = owned.Close()
			return h.writeServiceError(c, "tail_application_logs", name, ctx.Err())
		}
		if decoded.err != nil {
			_ = owned.Close()
			return h.writeServiceError(c, "decode_application_logs", name, decoded.err)
		}
		entries = decoded.entries
	case <-ctx.Done():
		_ = owned.Close()
		<-result
		return h.writeServiceError(c, "tail_application_logs", name, ctx.Err())
	}
	if err := owned.Close(); err != nil {
		return h.writeServiceError(c, "close_application_logs", name, err)
	}
	if entries == nil {
		entries = make([]LogEntryResponse, 0)
	}
	return c.JSON(LogTailResponse{Logs: entries, Total: len(entries)})
}

func decodeTail(reader io.Reader, framing swarm.LogFraming, maxBytes int64, maxLines int) ([]LogEntryResponse, error) {
	decoder, err := newLogDecoder(reader, framing, maxBytes, maxBytes)
	if err != nil {
		return nil, err
	}
	entries := make([]LogEntryResponse, 0)
	for {
		entry, err := decoder.Next()
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, err
		}
		if len(entries) < maxLines {
			entries = append(entries, entry)
		}
	}
}

func (h *LogsHandler) Stream(c fiber.Ctx) error {
	name, tail, err := h.inputs(c, h.options.InitialTail, true)
	if err != nil {
		if errors.Is(err, errLogResponseWritten) {
			return nil
		}
		return err
	}
	lease, ok := h.limiter.acquire(name)
	if !ok {
		c.Set(fiber.HeaderRetryAfter, "1")
		return apiresponse.WriteError(c, fiber.StatusTooManyRequests, "log_stream_limit", "runtime log stream limit reached", apiresponse.ErrorOptions{Retryable: true})
	}
	streamCtx, cancel := context.WithTimeout(context.Background(), h.options.MaxDuration)
	connection := c.RequestCtx().Conn()
	stopDeadlineAbort := context.AfterFunc(streamCtx, func() {
		if errors.Is(streamCtx.Err(), context.DeadlineExceeded) && connection != nil {
			_ = connection.Close()
		}
	})
	abort := func() {
		cancel()
		if connection != nil {
			_ = connection.Close()
		}
	}
	streamID, ok := h.registerStream(abort)
	if !ok {
		stopDeadlineAbort()
		cancel()
		lease.Release()
		return apiresponse.WriteError(c, fiber.StatusServiceUnavailable, "shutting_down", "server is shutting down", apiresponse.ErrorOptions{Retryable: true})
	}
	stream, err := h.openStream(streamCtx, name, swarm.LogRequest{Tail: tail, Follow: true})
	if err != nil {
		h.unregisterStream(streamID)
		stopDeadlineAbort()
		cancel()
		lease.Release()
		return h.writeServiceError(c, "stream_application_logs", name, err)
	}
	owned, err := validOwnedLogReader(stream)
	if err != nil {
		h.unregisterStream(streamID)
		stopDeadlineAbort()
		cancel()
		lease.Release()
		return h.writeServiceError(c, "stream_application_logs_response", name, err)
	}

	requestID := apiresponse.RequestID(c)
	started := time.Now()
	active, apps := h.limiter.snapshot()
	h.log.Info("runtime log stream opened", "request_id", requestID, "app", name, "active_streams", active, "active_apps", apps)
	c.Set(fiber.HeaderContentType, "text/event-stream; charset=utf-8")
	c.Set(fiber.HeaderCacheControl, "no-cache, no-store")
	c.Set("X-Accel-Buffering", "no")
	c.RequestCtx().SetBodyStreamWriter(func(writer *bufio.Writer) {
		reason := "panic"
		defer func() {
			if recovered := recover(); recovered != nil {
				h.log.Error("runtime log stream panicked", "request_id", requestID, "app", name)
			}
			stopDeadlineAbort()
			cancel()
			_ = owned.Close()
			h.unregisterStream(streamID)
			lease.Release()
			active, apps := h.limiter.snapshot()
			h.log.Info("runtime log stream closed", "request_id", requestID, "app", name, "reason", reason, "duration", time.Since(started), "active_streams", active, "active_apps", apps)
		}()
		reason = h.writeStream(writer, streamCtx, owned, stream.Framing, requestID)
	})
	return nil
}

func (h *LogsHandler) writeStream(writer *bufio.Writer, ctx context.Context, reader *ownedLogReader, framing swarm.LogFraming, requestID string) (reason string) {
	if _, err := writer.WriteString(": connected\n\n"); err != nil {
		return "write_error"
	}
	if err := writer.Flush(); err != nil {
		return "write_error"
	}
	results := make(chan decodeResult, 1)
	decoderCtx, stopDecoder := context.WithCancel(ctx)
	decoderDone := make(chan struct{})
	go func() {
		defer close(decoderDone)
		streamDecoded(decoderCtx, reader.reader, framing, h.options.MaxPayloadBytes, results)
	}()
	defer func() {
		stopDecoder()
		_ = reader.Close()
		<-decoderDone
	}()
	heartbeat := time.NewTicker(h.options.HeartbeatInterval)
	idle := time.NewTimer(h.options.IdleTimeout)
	defer heartbeat.Stop()
	defer idle.Stop()

	for {
		select {
		case result := <-results:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) {
					return "eof"
				}
				if ctx.Err() == nil {
					_ = writeSSEError(writer, requestID, result.err)
				}
				return "upstream_error"
			}
			if result.entry == nil || writeSSELog(writer, *result.entry) != nil {
				return "write_error"
			}
			resetTimer(idle, h.options.IdleTimeout)
		case <-heartbeat.C:
			if _, err := writer.WriteString(": keepalive\n\n"); err != nil {
				return "write_error"
			}
			if err := writer.Flush(); err != nil {
				return "write_error"
			}
		case <-idle.C:
			return "idle_timeout"
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "max_duration"
			}
			return "shutdown"
		}
	}
}

func streamDecoded(ctx context.Context, reader io.Reader, framing swarm.LogFraming, maxBytes int64, output chan<- decodeResult) {
	defer func() {
		if recover() != nil {
			select {
			case output <- decodeResult{err: errInvalidLogState}:
			case <-ctx.Done():
			}
		}
	}()
	decoder, err := newLogDecoder(reader, framing, maxBytes, 0)
	if err != nil {
		select {
		case output <- decodeResult{err: err}:
		case <-ctx.Done():
		}
		return
	}
	for {
		entry, err := decoder.Next()
		result := decodeResult{err: err}
		if err == nil {
			result.entry = &entry
		}
		select {
		case output <- result:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func writeSSELog(writer *bufio.Writer, entry LogEntryResponse) error {
	payload, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "event: log\ndata: %s\n\n", payload); err != nil {
		return err
	}
	return writer.Flush()
}

func writeSSEError(writer *bufio.Writer, requestID string, cause error) error {
	response := LogStreamErrorResponse{Code: "log_stream_unavailable", Message: "runtime log stream ended unexpectedly", RequestID: requestID, Retryable: true}
	if errors.Is(cause, errMalformedLogData) || errors.Is(cause, errLogPayloadLimit) || errors.Is(cause, errInvalidLogFraming) {
		response.Code = "invalid_log_stream"
		response.Message = "runtime log stream is invalid"
		response.Retryable = false
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "event: error\ndata: %s\n\n", payload); err != nil {
		return err
	}
	return writer.Flush()
}

func (h *LogsHandler) inputs(c fiber.Ctx, defaultTail int, stream bool) (string, int, error) {
	name, ok := validApplicationName(c.Params("name"))
	if !ok {
		_ = writeInvalidPath(c, "name")
		return "", 0, errLogResponseWritten
	}
	if len(c.Body()) != 0 {
		_ = apiresponse.WriteError(c, fiber.StatusBadRequest, "invalid_request", "request body must be empty", apiresponse.ErrorOptions{})
		return "", 0, errLogResponseWritten
	}
	if stream && len(c.Request().Header.PeekAll("Last-Event-ID")) != 0 {
		_ = apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "Last-Event-ID is unsupported", apiresponse.ErrorOptions{Fields: map[string]string{"Last-Event-ID": "runtime log streams are not replayable"}})
		return "", 0, errLogResponseWritten
	}
	values, err := url.ParseQuery(string(c.Request().URI().QueryString()))
	if err != nil {
		return "", 0, invalidTail(c)
	}
	for key, items := range values {
		if key != "tail" || len(items) != 1 || items[0] == "" {
			return "", 0, invalidTail(c)
		}
	}
	tail := defaultTail
	if items, exists := values["tail"]; exists {
		if len(items[0]) > 1 && items[0][0] == '0' {
			return "", 0, invalidTail(c)
		}
		for _, character := range items[0] {
			if character < '0' || character > '9' {
				return "", 0, invalidTail(c)
			}
		}
		tail, err = strconv.Atoi(items[0])
		if err != nil || tail < 1 || tail > h.options.MaxTail {
			return "", 0, invalidTail(c)
		}
	}
	// Fiber route parameters alias request-owned memory unless Immutable is
	// enabled. Log streams retain the name after the handler returns.
	return strings.Clone(name), tail, nil
}

func invalidTail(c fiber.Ctx) error {
	_ = apiresponse.WriteError(c, fiber.StatusUnprocessableEntity, "validation_failed", "query parameter is invalid", apiresponse.ErrorOptions{Fields: map[string]string{"tail": "must be one canonical integer within the configured limit"}})
	return errLogResponseWritten
}

func (h *LogsHandler) openStream(ctx context.Context, name string, request swarm.LogRequest) (*swarm.LogStream, error) {
	type result struct {
		stream *swarm.LogStream
		err    error
	}
	resultCh := make(chan result)
	abandoned := make(chan struct{})
	openCtx, cancel := context.WithCancel(ctx)
	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, h.options.OpenTimeout)
	defer deadlineCancel()
	go func() {
		completed := result{}
		defer func() {
			if recover() != nil {
				completed = result{err: errInvalidLogState}
			}
			select {
			case resultCh <- completed:
			case <-abandoned:
				if completed.stream != nil && completed.stream.Reader != nil {
					_ = completed.stream.Reader.Close()
				}
			}
		}()
		completed.stream, completed.err = h.service.OpenApplicationLogs(openCtx, name, request)
	}()
	select {
	case completed := <-resultCh:
		if deadlineCtx.Err() != nil {
			if completed.stream != nil && completed.stream.Reader != nil {
				_ = completed.stream.Reader.Close()
			}
			cancel()
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, context.DeadlineExceeded
		}
		if completed.err != nil || completed.stream == nil || completed.stream.Reader == nil {
			if completed.stream != nil && completed.stream.Reader != nil {
				_ = completed.stream.Reader.Close()
			}
			cancel()
			return completed.stream, completed.err
		}
		completed.stream.Reader = &cancelOnCloseLogReader{reader: completed.stream.Reader, cancel: cancel}
		return completed.stream, nil
	case <-deadlineCtx.Done():
		close(abandoned)
		cancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, context.DeadlineExceeded
	}
}

func (r *cancelOnCloseLogReader) Read(payload []byte) (int, error) {
	return r.reader.Read(payload)
}

func (r *cancelOnCloseLogReader) Close() error {
	r.once.Do(func() {
		r.err = r.reader.Close()
		r.cancel()
	})
	return r.err
}

func validOwnedLogReader(stream *swarm.LogStream) (*ownedLogReader, error) {
	if stream == nil || stream.Reader == nil || stream.Framing != swarm.LogFramingMultiplexed {
		if stream != nil && stream.Reader != nil {
			_ = stream.Reader.Close()
		}
		return nil, errInvalidLogState
	}
	return &ownedLogReader{reader: stream.Reader}, nil
}

func (r *ownedLogReader) Close() error {
	r.once.Do(func() { r.err = r.reader.Close() })
	return r.err
}

func (h *LogsHandler) registerStream(cancel context.CancelFunc) (uint64, bool) {
	h.streamsMu.Lock()
	defer h.streamsMu.Unlock()
	if h.shuttingDown {
		return 0, false
	}
	h.nextStreamID++
	h.activeStreams[h.nextStreamID] = cancel
	return h.nextStreamID, true
}

func (h *LogsHandler) unregisterStream(id uint64) {
	h.streamsMu.Lock()
	delete(h.activeStreams, id)
	h.streamsMu.Unlock()
}

func (h *LogsHandler) CloseActiveStreams() {
	h.streamsMu.Lock()
	h.shuttingDown = true
	cancellations := make([]context.CancelFunc, 0, len(h.activeStreams))
	for _, cancel := range h.activeStreams {
		cancellations = append(cancellations, cancel)
	}
	h.streamsMu.Unlock()
	for _, cancel := range cancellations {
		cancel()
	}
}

func (h *LogsHandler) writeServiceError(c fiber.Ctx, operation, name string, err error) error {
	h.log.Error("runtime log request failed", "request_id", apiresponse.RequestID(c), "operation", operation, "app", name, "error_type", fmt.Sprintf("%T", err))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return apiresponse.WriteError(c, fiber.StatusNotFound, "application_not_found", "application not found", apiresponse.ErrorOptions{})
	case errors.Is(err, context.DeadlineExceeded):
		return apiresponse.WriteError(c, fiber.StatusGatewayTimeout, "runtime_timeout", "runtime log request timed out", apiresponse.ErrorOptions{Retryable: true})
	case errors.Is(err, errMalformedLogData), errors.Is(err, errLogPayloadLimit), errors.Is(err, errInvalidLogFraming):
		return apiresponse.WriteError(c, fiber.StatusBadGateway, "invalid_log_stream", "runtime log stream is invalid", apiresponse.ErrorOptions{Retryable: true})
	default:
		return apiresponse.WriteError(c, fiber.StatusServiceUnavailable, "runtime_logs_unavailable", "runtime logs are unavailable", apiresponse.ErrorOptions{Retryable: true})
	}
}

func resetTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}
