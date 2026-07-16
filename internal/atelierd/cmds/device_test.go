package cmds

import (
	"errors"
	"fmt"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/devicebank"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/forge"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, 0},
		{devicebank.ErrExhausted, ExitBankExhausted},
		{devicebank.ErrNotInitialized, ExitBankNotInitialized},
		{fmt.Errorf("acquire: %w", devicebank.ErrExhausted), ExitBankExhausted},
		{forge.ErrUnknownRun, ExitForgeUnknownRun},
		{forge.ErrInvalidPass, ExitForgeInvalidPass},
		{forge.ErrCampaignInvalid, ExitForgeCampaign},
		{forge.ErrWaveCap, ExitForgeWaveCap},
		{forge.ErrInvalidStaging, ExitForgeStaging},
		{forge.ErrAmbiguousRun, ExitForgeAmbiguousRun},
		{fmt.Errorf("wrapped: %w", forge.ErrInvalidStaging), ExitForgeStaging},
		{errors.New("anything else"), 1},
	}
	for _, tt := range tests {
		if got := ExitCode(tt.err); got != tt.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}
