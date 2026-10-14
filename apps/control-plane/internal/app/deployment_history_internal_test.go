package app

import (
	"errors"
	"testing"

	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
)

func TestDeploymentCursorRejectsCorruptApplicationIdentityAsServerData(t *testing.T) {
	for _, cursor := range []string{"", "bad"} {
		_, err := decodeDeploymentCursor(cursor, "corrupt-application-id")
		if !errors.Is(err, store.ErrInvalidData) || errors.Is(err, ErrInvalidDeploymentCursor) {
			t.Fatalf("cursor %q with corrupt application ID = %v", cursor, err)
		}
	}
}
