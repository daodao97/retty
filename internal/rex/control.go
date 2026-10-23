package rex

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
)

const MaxControlInput = 64 << 10

// SendInput never attaches, resizes, or retries. On transport failure the caller
// must read before deciding what to do: the write may already have happened.
func (c *Client) SendInput(sid string, input InputRequest) error {
	return c.call(Request{Op: "sendInput", SID: sid, Input: &input}, nil)
}

// ReportProgram shares OSC 7501 status and notification deduplication.
func (c *Client) ReportProgram(sid string, status ProgramStatus) error {
	if _, err := ValidateProgramStatus(status); err != nil {
		return err
	}
	return c.call(Request{Op: "programStatus", SID: sid, Status: &status}, nil)
}

// ValidateProgramStatus accepts the same bounded content as OSC 7501, rejecting
// malformed CLI fields rather than silently dropping them as a stream parser can.
func ValidateProgramStatus(r ProgramStatus) (ProgramStatus, error) {
	bad := func() (ProgramStatus, error) {
		return ProgramStatus{}, errors.New("invalid program status (see docs/program-status.md)")
	}
	if r.App != "" && (!statusToken(r.App) || len(r.App) > 32) {
		return bad()
	}
	if r.ID != "" {
		if len(r.ID) > 128 || len(strings.Split(r.ID, "/")) > 8 {
			return bad()
		}
		for _, part := range strings.Split(r.ID, "/") {
			if !statusToken(part) || len(part) > 32 {
				return bad()
			}
		}
	}
	if r.Kind != "" && (r.State != "blocked" || (r.Kind != "permission" && r.Kind != "question" && r.Kind != "auth")) {
		return bad()
	}
	if r.Progress != nil && (*r.Progress < 0 || *r.Progress > 100 || r.State != "working" && r.State != "blocked") {
		return bad()
	}
	parts := []string{"state=" + r.State}
	for _, pair := range [][2]string{{"id", r.ID}, {"app", r.App}, {"kind", r.Kind}} {
		if pair[1] != "" {
			parts = append(parts, pair[0]+"="+pair[1])
		}
	}
	for _, pair := range [][2]string{{"title", r.Title}, {"msg", r.Message}} {
		if pair[1] != "" {
			parts = append(parts, pair[0]+"="+base64.StdEncoding.EncodeToString([]byte(pair[1])))
		}
	}
	if r.Progress != nil {
		parts = append(parts, "progress="+strconv.Itoa(*r.Progress))
	}
	body := strings.Join(parts, ":")
	if len(body) > 4080 {
		return bad()
	}
	normal, ok := parseProgramStatus(body)
	if !ok || normal.State != r.State || normal.ID != r.ID || normal.App != r.App {
		return bad()
	}
	return normal, nil
}
