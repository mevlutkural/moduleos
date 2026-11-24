package operations

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

type Event struct {
	Run          string `json:"run"`
	Arch         string `json:"arch"`
	Source       string `json:"source"`
	Image        string `json:"image"`
	Stage        string `json:"stage"`
	Status       string `json:"status"`
	Reason       string `json:"reason"`
	ElapsedMS    int64  `json:"elapsed_ms"`
	ExpectedHTTP int    `json:"expected_http,omitempty"`
	ObservedHTTP int    `json:"observed_http,omitempty"`
}

var stages = map[string]bool{"setup": true, "boot": true, "lifecycle": true, "deploy": true, "logs": true, "restart": true, "ownership": true, "outage": true, "routing": true, "restore": true, "delete": true, "cleanup": true, "final": true}
var reasons = map[string]bool{"none": true, "setup_failed": true, "infrastructure": true, "timeout": true, "product_mismatch": true, "cleanup_failed": true, "cancelled": true, "runner_failed": true}
var identity = regexp.MustCompile(`^(?:[a-f0-9]{12}|pending)$`)
var revision = regexp.MustCompile(`^(?:[a-f0-9]{40}|unknown)$`)
var artifact = regexp.MustCompile(`^(?:sha256:[a-f0-9]{64}|unknown)$`)

func (e Event) Validate() error {
	if !identity.MatchString(e.Run) || (e.Arch != "amd64" && e.Arch != "unknown") || !revision.MatchString(e.Source) || !artifact.MatchString(e.Image) || !stages[e.Stage] || !reasons[e.Reason] || e.ElapsedMS < 0 {
		return errors.New("invalid_evidence")
	}
	if e.ExpectedHTTP < 0 || e.ExpectedHTTP > 599 || e.ObservedHTTP < 0 || e.ObservedHTTP > 599 {
		return errors.New("invalid_evidence")
	}
	switch e.Status {
	case "started", "passed", "failed":
		return nil
	default:
		return errors.New("invalid_evidence")
	}
}
func AppendEvent(path string, e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return errors.New("invalid_evidence_file")
	}
	if err != nil && !os.IsNotExist(err) {
		return errors.New("invalid_evidence_file")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("evidence_write")
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(e)
}
func Summary(input io.Reader, output io.Writer) error {
	data, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("invalid_evidence")
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 8192)
	var events []Event
	for scanner.Scan() {
		var e Event
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&e); err != nil {
			return errors.New("invalid_evidence")
		}
		if err := e.Validate(); err != nil {
			return err
		}
		if decoder.Decode(new(any)) != io.EOF {
			return errors.New("invalid_evidence")
		}
		events = append(events, e)
	}
	if scanner.Err() != nil || len(events) == 0 {
		return errors.New("invalid_evidence")
	}
	for _, e := range events {
		fmt.Fprintf(output, "- %s: %s (%s, %d ms); run=%s, arch=%s, source=%s, image=%s\n", e.Stage, e.Status, e.Reason, e.ElapsedMS, e.Run, e.Arch, e.Source, e.Image)
	}
	fmt.Fprintln(output, "\n```jsonl")
	for _, e := range events {
		if json.NewEncoder(output).Encode(e) != nil {
			return errors.New("evidence_summary_write")
		}
	}
	fmt.Fprintln(output, "```")
	return nil
}
