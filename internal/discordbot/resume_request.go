package discordbot

import (
	"asterinmusic/internal/authorization"
	"context"
	"errors"

	"github.com/disgoorg/snowflake/v2"
)

type resumeState string

const (
	resumeAccepted     resumeState = "accepted"
	resumePlaying      resumeState = "playing"
	resumePreparing    resumeState = "preparing"
	resumeRecovering   resumeState = "recovering"
	resumeStarted      resumeState = "started"
	resumeEmpty        resumeState = "empty"
	resumeCanceled     resumeState = "canceled"
	resumeMoved        resumeState = "moved"
	resumeSourceFailed resumeState = "source_failed"
	resumeFailed       resumeState = "failed"
)

type resumeResult struct {
	state resumeState
	err   error
}

func (r resumeResult) message() string {
	if r.err != nil {
		return userError(r.err)
	}
	switch r.state {
	case resumeAccepted:
		return "再開要求を受け付けました。再生を準備しています。"
	case resumePlaying:
		return "既に再生中です。"
	case resumePreparing:
		return "再生を準備しています。"
	case resumeRecovering:
		return "音声接続を復旧しています。"
	case resumeStarted:
		return "キューを再開しました。"
	case resumeEmpty:
		return "再生する曲がありません。"
	case resumeMoved:
		return "VCが移動されたため、この応答の更新を終了しました。再生処理は移動先で継続します。"
	case resumeSourceFailed:
		return "音源の取得・再生に失敗したため、再開できませんでした。"
	case resumeFailed:
		return "音声接続に失敗したため、再開できませんでした。曲とキューは保持しています。"
	default:
		return "再開を中止しました。"
	}
}

// The playback request survives a VC move; its response does not. All fields
// are protected by session.mu, except the buffered result channel.
type resumeRequest struct {
	channel                               snowflake.ID
	result                                chan resumeResult
	launched, responseEnded, sourceFailed bool
}

func (r *resumeRequest) respond(result resumeResult) {
	if r == nil || r.responseEnded {
		return
	}
	r.responseEnded = true
	r.result <- result
}

func (s *session) finishResumeLocked(result resumeResult) {
	if r := s.resumeRequest; r != nil {
		r.respond(result)
		s.resumeRequest = nil
	}
}

func (s *session) requestResume(user, text snowflake.ID) (resumeResult, *resumeRequest, error) {
	channel, err := s.authorize(user)
	if err != nil {
		return resumeResult{}, nil, err
	}
	if err := s.lockAuthorized(user, authorization.PlaybackResume); err != nil {
		return resumeResult{}, nil, err
	}
	defer s.mu.Unlock()
	if err := s.mutableLocked(); err != nil {
		return resumeResult{}, nil, err
	}
	if s.channel != 0 && s.channel != channel {
		return resumeResult{}, nil, errVoiceChanged
	}
	if c, ok := s.conn.(*voiceConnection); ok {
		c.mu.Lock()
		recovering := c.phase == voiceRecovering || (c.recovery != nil && !c.recovery.explicit)
		c.mu.Unlock()
		if recovering {
			return resumeResult{state: resumeRecovering}, nil, nil
		}
	}
	if s.resumeRequest != nil || s.connecting {
		return resumeResult{state: resumePreparing}, nil, nil
	}
	if s.running && !s.stopped {
		if s.started {
			return resumeResult{state: resumePlaying}, nil, nil
		}
		return resumeResult{state: resumePreparing}, nil, nil
	}
	if s.closing != nil {
		return resumeResult{}, nil, errStopping
	}
	current, pending := s.queue.Snapshot()
	if len(pending) == 0 && !(s.recoveryHeld && current != nil) && s.resumeAuto == nil && (!s.auto.enabled || s.auto.seed == nil) {
		return resumeResult{state: resumeEmpty}, nil, nil
	}
	if s.trackCancel != nil {
		s.trackCancel()
	}
	s.auto.invalidate()
	r := &resumeRequest{channel: channel, result: make(chan resumeResult, 1)}
	s.resumeRequest = r
	s.text, s.accessVoice = text, channel
	return resumeResult{state: resumeAccepted}, r, nil
}

// Called only after the acceptance response has been attempted. A failed
// response update never owns cancellation of playback.
func (s *session) launchResume(r *resumeRequest) {
	s.mu.Lock()
	if s.resumeRequest != r || r.launched {
		s.mu.Unlock()
		return
	}
	r.launched = true
	s.mu.Unlock()
	go func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.resumeRequest != r {
			return
		}
		channel := s.channel
		if channel == 0 {
			channel = r.channel
		}
		if err := s.startLocked(channel); err != nil && s.resumeRequest == r {
			result := resumeResult{state: resumeFailed, err: err}
			if errors.Is(err, errVoiceConnection) {
				result.err = nil
			}
			if errors.Is(err, errQueueEmpty) {
				result = resumeResult{state: resumeEmpty}
			}
			if errors.Is(err, errQueueChanged) || errors.Is(err, context.Canceled) {
				result = resumeResult{state: resumeCanceled}
			}
			s.finishResumeLocked(result)
		}
	}()
}
