package handler

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"sort"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

var (
	errInvalidLogFraming  = errors.New("invalid runtime log framing")
	errLogPayloadLimit    = errors.New("runtime log payload exceeds limit")
	errMalformedLogData   = errors.New("runtime log data is malformed")
	errRuntimeLogUpstream = errors.New("runtime log upstream reported an error")
)

type pendingLogLine struct {
	data    []byte
	lastSeq uint64
}

type logDecoder struct {
	reader         *bufio.Reader
	maxEntryBytes  int64
	maxTotalBytes  int64
	totalBytes     int64
	sequence       uint64
	pending        map[string]*pendingLogLine
	queue          []LogEntryResponse
	bufferedStream string
	bufferedChunk  []byte
	eof            bool
}

func newLogDecoder(reader io.Reader, framing swarm.LogFraming, maxEntryBytes, maxTotalBytes int64) (*logDecoder, error) {
	if reader == nil || maxEntryBytes < 1 || maxTotalBytes < 0 {
		return nil, errInvalidLogFraming
	}
	if framing != swarm.LogFramingMultiplexed {
		return nil, errInvalidLogFraming
	}
	bufferSize := int(min(maxEntryBytes+1, 64*1024))
	return &logDecoder{
		reader:        bufio.NewReaderSize(reader, bufferSize),
		maxEntryBytes: maxEntryBytes, maxTotalBytes: maxTotalBytes,
		pending: make(map[string]*pendingLogLine),
	}, nil
}

func (d *logDecoder) Next() (LogEntryResponse, error) {
	for {
		if len(d.queue) > 0 {
			entry := d.queue[0]
			d.queue = d.queue[1:]
			return entry, nil
		}
		if len(d.bufferedChunk) > 0 {
			chunk := d.bufferedChunk
			stream := d.bufferedStream
			d.bufferedChunk = nil
			d.bufferedStream = ""
			if err := d.consume(stream, chunk); err != nil {
				return LogEntryResponse{}, err
			}
			continue
		}
		if d.eof {
			return LogEntryResponse{}, io.EOF
		}
		err := d.readMultiplexed()
		if errors.Is(err, io.EOF) {
			d.flushPending()
			d.eof = true
			continue
		}
		if err != nil {
			return LogEntryResponse{}, err
		}
	}
}

func (d *logDecoder) readMultiplexed() error {
	header := make([]byte, 8)
	count, err := io.ReadFull(d.reader, header)
	if errors.Is(err, io.EOF) && count == 0 {
		return io.EOF
	}
	if err != nil {
		if count == 0 && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
		return errMalformedLogData
	}
	if (header[0] < 1 || header[0] > 3) || header[1] != 0 || header[2] != 0 || header[3] != 0 {
		return errMalformedLogData
	}
	size := int64(binary.BigEndian.Uint32(header[4:]))
	if size == 0 {
		if header[0] == 3 {
			return errRuntimeLogUpstream
		}
		return errMalformedLogData
	}
	if size > d.maxEntryBytes {
		return errLogPayloadLimit
	}
	if err := d.noteBytes(size); err != nil {
		return err
	}
	payload := make([]byte, size)
	count, err = io.ReadFull(d.reader, payload)
	if err != nil {
		if count == 0 && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
		return errMalformedLogData
	}
	if header[0] == 3 {
		return errRuntimeLogUpstream
	}
	stream := "stdout"
	if header[0] == 2 {
		stream = "stderr"
	}
	return d.consume(stream, payload)
}

func (d *logDecoder) noteBytes(size int64) error {
	if size < 0 || (d.maxTotalBytes > 0 && (size > d.maxTotalBytes || d.totalBytes > d.maxTotalBytes-size)) {
		return errLogPayloadLimit
	}
	d.totalBytes += size
	return nil
}

func (d *logDecoder) consume(stream string, chunk []byte) error {
	pending := d.pending[stream]
	if pending == nil {
		pending = &pendingLogLine{}
		d.pending[stream] = pending
	}
	for {
		newline := -1
		for index, value := range chunk {
			if value == '\n' {
				newline = index
				break
			}
		}
		segment := chunk
		if newline >= 0 {
			segment = chunk[:newline]
		}
		if int64(len(pending.data))+int64(len(segment)) > d.maxEntryBytes {
			return errLogPayloadLimit
		}
		pending.data = append(pending.data, segment...)
		d.sequence++
		pending.lastSeq = d.sequence
		if newline < 0 {
			return nil
		}
		message := pending.data
		if len(message) > 0 && message[len(message)-1] == '\r' {
			message = message[:len(message)-1]
		}
		d.queue = append(d.queue, LogEntryResponse{Stream: stream, Message: string(message)})
		pending.data = nil
		chunk = chunk[newline+1:]
		if len(chunk) > 0 {
			d.bufferedStream = stream
			d.bufferedChunk = chunk
		}
		return nil
	}
}

func (d *logDecoder) flushPending() {
	type pendingEntry struct {
		stream string
		line   *pendingLogLine
	}
	entries := make([]pendingEntry, 0, len(d.pending))
	for stream, line := range d.pending {
		if len(line.data) > 0 {
			entries = append(entries, pendingEntry{stream: stream, line: line})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].line.lastSeq < entries[j].line.lastSeq })
	for _, entry := range entries {
		d.queue = append(d.queue, LogEntryResponse{Stream: entry.stream, Message: string(entry.line.data)})
	}
	clear(d.pending)
}
