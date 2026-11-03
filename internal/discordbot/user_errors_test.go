package discordbot

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestUserErrorClassificationIgnoresWrapperText(t *testing.T) {
	for code := errJoinVoice; code <= errQueueSave; code++ {
		err := fmt.Errorf("private context: %w", code)
		if got := userError(err); got != code.Error() || strings.Contains(got, "private") {
			t.Fatalf("code %d lost its safe presentation", code)
		}
		if !errors.Is(err, code) {
			t.Fatal("code identity lost")
		}
		if got := userError(errors.New(code.Error())); got == code.Error() {
			t.Fatalf("untyped text was trusted for code %d", code)
		}
	}
}
