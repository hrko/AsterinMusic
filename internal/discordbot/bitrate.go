package discordbot

// Gateway channel updates refresh disgo's cache before dispatching listeners.
// Reading the latest cache avoids out-of-order asynchronous update callbacks
// and performs no network IO on the audio loop.
func (s *session) channelBitrate() int {
	s.mu.Lock()
	id := s.channel
	if c, ok := s.conn.(*voiceConnection); ok {
		if current := c.ChannelID(); current != nil {
			id = *current
		}
	}
	s.mu.Unlock()
	channel, ok := s.app.client.Caches.GuildAudioChannel(id)
	if !ok {
		return 0
	}
	return channel.Bitrate()
}
