package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"time"

	"retty/internal/approval"
	"retty/internal/rex"
)

type approvalRequest struct {
	Agent  string `json:"agent,omitempty"`
	Op     string `json:"op"`
	SID    string `json:"sid"`
	Thread string `json:"thread"`
	ID     string `json:"requestID,omitempty"`
	Choice string `json:"choice,omitempty"`
}

func (b *Bridge) serveApproval(conn net.Conn, socket string, line []byte) {
	ctx, cancel := context.WithTimeout(b.imageContext, 4*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(4 * time.Second))
	res := rex.Response{}
	var req approvalRequest
	var requests []approval.Request
	var agent, thread string
	err := json.Unmarshal(line, &req)
	if err == nil && (len(line) > 4096 || req.SID == "" || len(req.SID) > 256 || len(req.Thread) > 256) {
		err = errors.New("invalid approval request")
	}
	if err == nil {
		// Validate the live pane. Only hook/relay metadata names an actual
		// conversation; an OSC program status cannot be used as a thread ID.
		var c net.Conn
		c, err = (&net.Dialer{}).DialContext(ctx, "unix", socket)
		if err == nil {
			c.SetDeadline(time.Now().Add(2 * time.Second))
			client := rex.NewClient(c, nil)
			var list []rex.SessionInfo
			list, err = client.List()
			client.Close()
			if err == nil {
				valid := false
				for _, s := range list {
					if s.ID == req.SID {
						agent, thread = rex.ApprovalIdentity(s)
						valid = agent != "" && (req.Agent == "" || req.Agent == agent) && (thread == "" || req.Thread == "" || thread == req.Thread)
						break
					}
				}
				if !valid {
					err = approval.ErrStale
				}
			}
		}
	}
	if err == nil {
		dir := b.approvalDir
		if dir == "" {
			dir = rex.Dir()
		}
		if req.ID == "" && req.Choice == "" {
			queryThread := req.Thread
			if queryThread == "" {
				queryThread = thread
			}
			requests, err = approval.List(ctx, dir, req.SID, queryThread)
			filtered := requests[:0]
			for _, r := range requests {
				if r.Agent == agent {
					filtered = append(filtered, r)
				}
			}
			requests = filtered
		} else if req.ID != "" && req.Choice != "" && req.Thread != "" {
			err = approval.Answer(ctx, dir, req.SID, req.Thread, req.ID, req.Choice)
		} else {
			err = errors.New("invalid approval response")
		}
	}
	if err != nil {
		res.Error = err.Error()
	} else {
		res.Data, _ = json.Marshal(requests)
	}
	_ = json.NewEncoder(conn).Encode(res)
}

func approvalExchange(ctx context.Context, client *rex.Client, req approvalRequest) ([]approval.Request, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := client.DialExtension(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	d, _ := ctx.Deadline()
	conn.SetDeadline(d)
	req.Op = "agent-approval"
	if err = json.NewEncoder(conn).Encode(req); err != nil {
		return nil, err
	}
	var res rex.Response
	if err = json.NewDecoder(io.LimitReader(conn, 3<<20)).Decode(&res); err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	var requests []approval.Request
	err = json.Unmarshal(res.Data, &requests)
	return requests, err
}
func ListApprovals(ctx context.Context, client *rex.Client, sid, thread string) ([]approval.Request, error) {
	return approvalExchange(ctx, client, approvalRequest{SID: sid, Thread: thread})
}
func AnswerApproval(ctx context.Context, client *rex.Client, r approval.Request, choice string) error {
	_, err := approvalExchange(ctx, client, approvalRequest{Agent: r.Agent, SID: r.SID, Thread: r.ThreadID, ID: r.ID, Choice: choice})
	return err
}
