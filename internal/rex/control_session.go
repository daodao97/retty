//go:build darwin || linux

package rex

import (
	"errors"
	"io"
	"time"
)

func (s *session) sendInput(input *InputRequest) error {
	if input == nil || len(input.Data) == 0 || len(input.Data) > MaxControlInput {
		return errors.New("input must contain 1..65536 bytes")
	}
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	s.mu.Lock()
	if s.exited {
		s.mu.Unlock()
		return errors.New("session has exited")
	}
	if input.IfOutput != nil && *input.IfOutput != s.output {
		s.mu.Unlock()
		return errors.New("session output changed; read again before sending")
	}
	s.lastInput = time.Now()
	s.mu.Unlock()
	// A program may stop reading its terminal. Bound this one attempt; partial
	// writes are reported and must never cause an automatic whole-input retry.
	if err := s.p.master.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	defer s.p.master.SetWriteDeadline(time.Time{})
	n, err := s.p.master.Write(input.Data)
	if err == nil && n != len(input.Data) {
		err = io.ErrShortWrite
	}
	return err
}

func (s *session) reportProgram(status *ProgramStatus) error {
	if status == nil {
		return errors.New("program status is required")
	}
	normal, err := ValidateProgramStatus(*status)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.exited {
		return errors.New("session has exited")
	}
	s.programStatus.serial = max(s.programStatus.serial, s.agent.state.WaitRevision, s.agent.state.CompletionRevision)
	s.programStatus.apply(normal, time.Now())
	return nil
}
