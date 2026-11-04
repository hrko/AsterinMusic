package discordbot

import "github.com/disgoorg/snowflake/v2"

// responseRef contains a credential; never log or serialize it. The operation
// identifies the current action even when editing an older interaction reply.
type responseRef struct {
	token     string
	operation snowflake.ID
}

func responseFor(e interface {
	ID() snowflake.ID
	Token() string
}) responseRef {
	return responseRef{token: e.Token(), operation: e.ID()}
}
