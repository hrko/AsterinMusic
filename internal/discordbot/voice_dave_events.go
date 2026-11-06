package discordbot

import (
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/godave"
)

// Called by the ordered transport worker. guardedDaveSession serializes DAVE
// events with encryption; transport cleanup waits for this worker to finish.
// Protocol payload types are supplied by disgo.
func handleVoiceDaveEvent(s godave.Session, op voice.Opcode, data voice.GatewayMessageData) {
	switch op {
	case voice.OpcodeSessionDescription:
		s.OnSelectProtocolAck(uint16(data.(voice.GatewayMessageDataSessionDescription).DaveProtocolVersion))
	case voice.OpcodeClientsConnect:
		for _, id := range data.(voice.GatewayMessageDataClientsConnect).UserIDs {
			s.AddUser(godave.UserID(id.String()))
		}
	case voice.OpcodeClientDisconnect:
		s.RemoveUser(godave.UserID(data.(voice.GatewayMessageDataClientDisconnect).UserID.String()))
	case voice.OpcodeDavePrepareTransition:
		d := data.(voice.GatewayMessageDataDaveProtocolPrepareTransition)
		s.OnDavePrepareTransition(d.TransitionID, d.ProtocolVersion)
	case voice.OpcodeDaveExecuteTransition:
		s.OnDaveExecuteTransition(data.(voice.GatewayMessageDataDaveProtocolExecuteTransition).TransitionID)
	case voice.OpcodeDavePrepareEpoch:
		d := data.(voice.GatewayMessageDataDaveProtocolPrepareEpoch)
		s.OnDavePrepareEpoch(d.Epoch, d.ProtocolVersion)
	case voice.OpcodeDaveMLSExternalSenderPackage:
		s.OnDaveMLSExternalSenderPackage(data.(voice.GatewayMessageDataDaveMLSExternalSenderPackage))
	case voice.OpcodeDaveMLSProposals:
		s.OnDaveMLSProposals(data.(voice.GatewayMessageDataDaveMLSProposals))
	case voice.OpcodeDaveMLSPrepareCommitTransition:
		d := data.(voice.GatewayMessageDataDaveMLSAnnounceCommitTransition)
		s.OnDaveMLSPrepareCommitTransition(d.TransitionID, d.CommitMessage)
	case voice.OpcodeDaveMLSWelcome:
		d := data.(voice.GatewayMessageDataDaveMLSWelcome)
		s.OnDaveMLSWelcome(d.TransitionID, d.WelcomeMessage)
	}
}
