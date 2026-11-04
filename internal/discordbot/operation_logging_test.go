package discordbot

import (
	"asterinmusic/internal/logging"
	"encoding/json"
	"errors"
	"github.com/disgoorg/snowflake/v2"
	"strings"
	"sync"
	"testing"
)

func TestConcurrentOperationCorrelation(t *testing.T) {
	var out logBuffer
	l, _ := logging.New(&out, logging.Options{Format: "json", Level: "debug"})
	a := &App{ctx: t.Context(), logger: l}
	a.config.GuildID = 3
	var wg sync.WaitGroup
	for _, id := range []snowflake.ID{10, 20} {
		wg.Go(func() {
			defer a.observeOperation(id, "command")()
			a.logInteractionFailure(id, "interaction.reply", errors.New("PRIVATE_TOKEN"))
		})
	}
	wg.Wait()
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["guild_id"] != "3" {
			t.Fatalf("missing scope: %v", event)
		}
		counts[event["operation_id"].(string)]++
	}
	if counts["10"] != 3 || counts["20"] != 3 || strings.Contains(out.String(), "PRIVATE_TOKEN") {
		t.Fatalf("incorrect correlation: %s", out.String())
	}
}
