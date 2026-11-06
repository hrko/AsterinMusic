//go:build davego

package discordbot

import (
	"asterinmusic/internal/logging"
	"bytes"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	davesession "github.com/thomas-vilte/dave-go/session"
	"log/slog"
	"net"
	"sync"
)

const daveEnabled = true
const daveBackend = "dave-go v0.5.1"

func daveOptions(a *App) []bot.ConfigOpt {
	return []bot.ConfigOpt{bot.WithVoiceManagerConfigOpts(voice.WithDaveSessionCreateFunc(func(logger *slog.Logger, user godave.UserID, callbacks godave.Callbacks) godave.Session {
		ownLogger := logging.Component(a.logger, "dave")
		factory := func() *davesession.Session {
			return davesession.New(user, callbacks, davesession.WithLogger(logging.Dependency(ownLogger, "dave")))
		}
		s := &guardedDave{Session: factory(), logger: ownLogger}
		a.daveClosers.Store(callbacks, s)
		return s
	}))}
}

// Serialize protocol mutations with the readiness check and Encrypt. Without
// this gate an epoch reset between the check and Encrypt could emit plaintext.
type guardedDave struct {
	logger *slog.Logger
	*davesession.Session
	mu sync.Mutex

	closed bool
}

// Only negotiated state and counters; never expose MLS payloads or keys.
func (s *guardedDave) VoiceDiagnostic() []any {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, stats := s.State(), s.Stats()
	return []any{"dave_protocol", state.ProtocolVersion, "dave_epoch_ready", state.Ready,
		"dave_epoch", state.EpochID, "dave_commits", stats.CommitsProcessed,
		"dave_commits_failed", stats.CommitsFailed, "dave_welcomes", stats.WelcomesJoined,
		"dave_welcomes_failed", stats.WelcomesFailed, "dave_recoveries", stats.RecoveryAttempts,
		"dave_proposals_rejected", stats.ProposalsRejected}
}

func (s *guardedDave) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.closed && !s.Session.ShouldHoldFrames()
}
func (s *guardedDave) MaxSupportedProtocolVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Session.MaxSupportedProtocolVersion()
}
func (s *guardedDave) AssignSsrcToCodec(id uint32, codec godave.Codec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.AssignSsrcToCodec(id, codec)
}
func (s *guardedDave) MaxEncryptedFrameSize(n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Session.MaxEncryptedFrameSize(n)
}
func (s *guardedDave) MaxDecryptedFrameSize(id godave.UserID, n int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Session.MaxDecryptedFrameSize(id, n)
}
func (s *guardedDave) Decrypt(id godave.UserID, p, out []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, net.ErrClosed
	}
	return s.Session.Decrypt(id, p, out)
}

func (s *guardedDave) Encrypt(ssrc uint32, p, out []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ShouldHoldFrames() {
		return 0, errDaveNotReady
	}
	encrypted := s.State().ProtocolVersion != 0
	n, err := s.Session.Encrypt(ssrc, p, out)
	// Upstream recovery watchdogs also mutate epochs internally. Reject their
	// passthrough result even if a reset races with the readiness check above.
	if err == nil && encrypted && bytes.Equal(p, out[:n]) {
		return 0, errDaveNotReady
	}
	return n, err
}
func (s *guardedDave) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	stats := s.Stats()
	err := s.Session.Close()
	s.mu.Unlock()
	logging.OrDiscard(s.logger).Debug("DAVE session closed", "event", "dave.session.closed", "backend", daveBackend, "commits", stats.CommitsProcessed, "welcomes", stats.WelcomesJoined, "encrypt_failures", stats.EncryptFailures, "passthrough_frames", stats.PassthroughFrames)
	return err
}
func (s *guardedDave) SetChannelID(id godave.ChannelID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.SetChannelID(id)
}
func (s *guardedDave) AddUser(id godave.UserID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.AddUser(id)
}
func (s *guardedDave) RemoveUser(id godave.UserID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.RemoveUser(id)
}
func (s *guardedDave) OnSelectProtocolAck(v uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.OnSelectProtocolAck(v)
}
func (s *guardedDave) OnDavePrepareTransition(id, v uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.OnDavePrepareTransition(id, v)
}
func (s *guardedDave) OnDaveExecuteTransition(id uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.OnDaveExecuteTransition(id)
}
func (s *guardedDave) OnDavePrepareEpoch(epoch int, v uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.OnDavePrepareEpoch(epoch, v)
}
func (s *guardedDave) OnDaveMLSExternalSenderPackage(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.OnDaveMLSExternalSenderPackage(p)
}
func (s *guardedDave) OnDaveMLSProposals(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.OnDaveMLSProposals(p)
}
func (s *guardedDave) OnDaveMLSPrepareCommitTransition(id uint16, p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.OnDaveMLSPrepareCommitTransition(id, p)
}
func (s *guardedDave) OnDaveMLSWelcome(id uint16, p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.Session.OnDaveMLSWelcome(id, p)
}
