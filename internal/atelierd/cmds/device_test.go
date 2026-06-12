package cmds

import (
	"errors"
	"fmt"
	"testing"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/devicebank"
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
		{errors.New("anything else"), 1},
	}
	for _, tt := range tests {
		if got := ExitCode(tt.err); got != tt.want {
			t.Errorf("ExitCode(%v) = %d, want %d", tt.err, got, tt.want)
		}
	}
}
