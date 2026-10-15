package handler

type LogEntryResponse struct {
	Stream  string `json:"stream"`
	Message string `json:"message"`
}

type LogTailResponse struct {
	Logs  []LogEntryResponse `json:"logs"`
	Total int                `json:"total"`
}

type LogStreamErrorResponse struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Retryable bool   `json:"retryable"`
}
