//go:build davego

package discordbot

import (
	"bytes"
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
	davesession "github.com/thomas-vilte/dave-go/session"
	"log/slog"
	"sync"
)

const daveEnabled = true
const daveBackend = "dave-go v0.5.1"

func daveOptions(a *App) []bot.ConfigOpt {
	return []bot.ConfigOpt{bot.WithVoiceManagerConfigOpts(voice.WithDaveSessionCreateFunc(func(logger *slog.Logger, user godave.UserID, callbacks godave.Callbacks) godave.Session {
		factory := func() *davesession.Session { return davesession.New(user, callbacks, davesession.WithLogger(logger)) }
		s := &guardedDave{Session: factory()}
		a.daveClosers.Store(callbacks, s)
		return s
	}))}
}

// Serialize protocol mutations with the readiness check and Encrypt. Without
// this gate an epoch reset between the check and Encrypt could emit plaintext.
type guardedDave struct {
	*davesession.Session
	mu sync.Mutex

	closed bool
}

func (s *guardedDave) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.Session.ShouldHoldFrames()
}
func (s *guardedDave) MaxSupportedProtocolVersion() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Session.MaxSupportedProtocolVersion()
}
func (s *guardedDave) AssignSsrcToCodec(id uint32, codec godave.Codec) {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	return s.Session.Decrypt(id, p, out)
}

func (s *guardedDave) Encrypt(ssrc uint32, p, out []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ShouldHoldFrames() {
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
	defer s.mu.Unlock()
	s.closed = true
	stats := s.Stats()
	slog.Info("DAVE session closed", "backend", daveBackend, "commits", stats.CommitsProcessed, "welcomes", stats.WelcomesJoined, "encrypt_failures", stats.EncryptFailures, "passthrough_frames", stats.PassthroughFrames)
	return s.Session.Close()
}
func (s *guardedDave) SetChannelID(id godave.ChannelID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.SetChannelID(id)
}
func (s *guardedDave) AddUser(id godave.UserID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.AddUser(id)
}
func (s *guardedDave) RemoveUser(id godave.UserID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.RemoveUser(id)
}
func (s *guardedDave) OnSelectProtocolAck(v uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.OnSelectProtocolAck(v)
}
func (s *guardedDave) OnDavePrepareTransition(id, v uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.OnDavePrepareTransition(id, v)
}
func (s *guardedDave) OnDaveExecuteTransition(id uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.OnDaveExecuteTransition(id)
}
func (s *guardedDave) OnDavePrepareEpoch(epoch int, v uint16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.OnDavePrepareEpoch(epoch, v)
}
func (s *guardedDave) OnDaveMLSExternalSenderPackage(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.OnDaveMLSExternalSenderPackage(p)
}
func (s *guardedDave) OnDaveMLSProposals(p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.OnDaveMLSProposals(p)
}
func (s *guardedDave) OnDaveMLSPrepareCommitTransition(id uint16, p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.OnDaveMLSPrepareCommitTransition(id, p)
}
func (s *guardedDave) OnDaveMLSWelcome(id uint16, p []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Session.OnDaveMLSWelcome(id, p)
}
