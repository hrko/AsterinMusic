package discordbot

import (
	"asterinmusic/internal/authorization"
	"asterinmusic/internal/music"
	"github.com/disgoorg/snowflake/v2"
	"time"
)

// queueEditTargetLocked binds a pending item to its current VC and owner.
// The caller holds session.mu. The session epoch is checked after external I/O.
func (s *session) queueEditTargetLocked(owner, id string) (authorizationTarget, error) {
	if s.conn != nil {
		s.syncVoiceLocked()
	}
	voice := s.channel
	if voice == 0 {
		voice = s.accessVoice
	}
	if voice == 0 {
		return authorizationTarget{}, errPermission
	}
	for _, entry := range s.queue.RequesterEntries(owner) {
		if entry.ID == id {
			return authorizationTarget{guild: s.guild, voice: voice, session: s.historySessionID, requester: owner, entry: id}, nil
		}
	}
	return authorizationTarget{}, music.ErrEntryNotWaiting
}

func (s *session) editQueueAs(user snowflake.ID, owner, id string, position int, expected []string, remove bool) error {
	return s.editQueueScopedAs(user, owner, id, position, expected, remove, nil)
}

type queueEditScope struct {
	voice   snowflake.ID
	epoch   uint64
	expires time.Time
}

func (s *session) editQueueScopedAs(user snowflake.ID, owner, id string, position int, expected []string, remove bool, scope *queueEditScope) error {
	action := authorization.QueueReorder
	if remove {
		action = authorization.QueueRemove
	}
	s.mu.Lock()
	s.waitQueueLocked()
	target, err := s.queueEditTargetLocked(owner, id)
	epoch := s.epoch
	if scope != nil && (target.voice != scope.voice || epoch != scope.epoch || !time.Now().Before(scope.expires)) {
		err = errQueueEditor
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err = s.app.authorizeTarget(s.app.ctx, user, action, target); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.mutableLocked(); err != nil {
		return err
	}
	latest, err := s.queueEditTargetLocked(owner, id)
	if err != nil {
		return err
	}
	if epoch != s.epoch || latest != target || s.queueSaving != nil {
		return errQueueChanged
	}
	state, ok := s.app.client.Caches.VoiceState(s.guild, user)
	if scope != nil && !time.Now().Before(scope.expires) {
		return errQueueEditor
	}
	if !ok || state.ChannelID == nil || *state.ChannelID != target.voice {
		return errPermission
	}
	s.queueIOLocked(func() {
		if remove {
			err = s.queue.RemoveWaiting(owner, id)
		} else {
			err = s.queue.MoveWaiting(owner, id, position, expected)
		}
	})
	return err
}
