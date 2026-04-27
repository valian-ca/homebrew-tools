package cmds

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/app"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/credentials"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/deviceauth"
	"github.com/valian-ca/homebrew-tools/internal/atelierd/firebaseauth"
)

const (
	deviceCodeAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	deviceCodeLength   = 8
	linkPollInterval   = 2 * time.Second
	linkTotalTimeout   = 5 * time.Minute
)

// NewLinkCmd builds the `atelierd link` sub-command.
func NewLinkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "link",
		Short: "Link this machine to a Valian Firebase account",
		Long: `Generate an 8-char device code, register it with the backend, open the
dashboard's connect-machine page in the browser, then poll for the user to
enter the code. On success, persist Firebase credentials at
~/.atelier/credentials (mode 0600).

Times out after 5 minutes if the user never enters the code.`,
		Args: cobra.NoArgs,
		RunE: runLink,
	}
}

func runLink(cmd *cobra.Command, _ []string) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), linkTotalTimeout)
	defer cancel()

	host, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("resolve hostname: %w", err)
	}

	code, err := registerDeviceCode(ctx, host)
	if err != nil {
		return err
	}

	cmd.Println()
	cmd.Println("    Linking code:  " + formatCode(code))
	cmd.Println()
	cmd.Println("Open the connection page and enter the code above.")
	cmd.Println("URL: " + app.DashboardConnectMachineURL(code))
	cmd.Println()
	if err := openBrowser(app.DashboardConnectMachineURL(code)); err != nil {
		// Browser launch is a convenience — the URL is still printed above.
		cmd.PrintErrln("(Note: could not open the browser automatically — " + err.Error() + ")")
	}
	cmd.Println("Waiting…")

	customToken, err := pollForLink(ctx, cmd, code)
	if err != nil {
		return err
	}

	signed, err := firebaseauth.SignInWithCustomToken(ctx, customToken)
	if err != nil {
		return fmt.Errorf("sign-in with custom token: %w", err)
	}

	creds := &credentials.Credentials{
		UID:              signed.UID,
		Email:            signed.Email,
		IDToken:          signed.IDToken,
		RefreshToken:     signed.RefreshToken,
		IDTokenExpiresAt: signed.IDTokenExpiresAt,
	}
	if err := credentials.Save(creds); err != nil {
		return fmt.Errorf("persist credentials: %w", err)
	}

	cmd.Println()
	cmd.Println("Linked as " + signed.Email + " on " + host + ". ✓")
	return nil
}

// registerDeviceCode generates a fresh code and registers it with the backend.
// Retries once on the rare "already-exists" collision.
func registerDeviceCode(ctx context.Context, host string) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		code, err := generateDeviceCode()
		if err != nil {
			return "", err
		}
		if err := deviceauth.CreateDeviceCode(ctx, code, host); err != nil {
			if deviceauth.IsCodeAlreadyExists(err) && attempt == 0 {
				continue
			}
			return "", fmt.Errorf("createDeviceCode: %w", err)
		}
		return code, nil
	}
	return "", errors.New("createDeviceCode: collision retries exhausted")
}

// pollForLink polls exchangeDeviceCode every linkPollInterval until the user
// links the code in the dashboard, the context is cancelled, or the total
// timeout elapses.
func pollForLink(ctx context.Context, cmd *cobra.Command, code string) (string, error) {
	ticker := time.NewTicker(linkPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				cmd.Println()
				return "", errors.New("Timed out. Re-run `atelierd link`.")
			}
			return "", ctx.Err()
		case <-ticker.C:
			res, err := deviceauth.ExchangeDeviceCode(ctx, code)
			if err != nil {
				// Transient errors: log and keep polling. Don't terminate the
				// link flow on a single hiccup.
				cmd.PrintErrln("(poll: " + err.Error() + ")")
				continue
			}
			if res.Linked {
				return res.CustomToken, nil
			}
		}
	}
}

func generateDeviceCode() (string, error) {
	buf := make([]byte, deviceCodeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("crypto/rand: %w", err)
	}
	out := make([]byte, deviceCodeLength)
	for i, b := range buf {
		out[i] = deviceCodeAlphabet[int(b)%len(deviceCodeAlphabet)]
	}
	return string(out), nil
}

// formatCode inserts a hyphen in the middle for visual readability ("ABCD-1234").
func formatCode(code string) string {
	if len(code) != deviceCodeLength {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// openBrowser launches the user's default browser at url. macOS-only in V1
// per stress-test decision (the spec acknowledges Linux/Windows are deferred).
func openBrowser(url string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("browser opening is macOS-only in V1")
	}
	return exec.Command("/usr/bin/open", url).Run()
}
