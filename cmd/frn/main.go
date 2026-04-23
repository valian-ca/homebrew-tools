// frn — flutter run, with a fast device picker.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/valian-ca/homebrew-tools/internal/device"
	"github.com/valian-ca/homebrew-tools/internal/flavor"
	"github.com/valian-ca/homebrew-tools/internal/state"
	"github.com/valian-ca/homebrew-tools/internal/ui"
)

// version is stamped in by the Homebrew formula via -ldflags.
var version = "dev"

const defaultVMServiceFile = ".dart_tool/valian/vmservice.uri"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx); err != nil {
		if errors.Is(err, ui.ErrCancelled) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// opts holds parsed CLI flags before they're reconciled with env vars and
// saved state. cliFlavor uses a nil *string so we can tell "unset" from
// "set empty" (the --no-flavor case).
type opts struct {
	cliFlavor     *string
	cliVMService  string
	extraArgs     []string
	showHelp      bool
	showVersion   bool
}

func run(ctx context.Context) error {
	o, err := parseArgs(os.Args[1:])
	if err != nil {
		return err
	}
	if o.showHelp {
		printUsage(os.Stdout, detectDefaults())
		return nil
	}
	if o.showVersion {
		fmt.Printf("frn %s\n", version)
		return nil
	}

	// Precedence (low → high): hardcoded defaults < env vars < state file < CLI args.
	flavors := flavor.Detect(".")
	sel := ui.Selection{
		Flavor:        defaultFlavor(flavors),
		VMServiceFile: defaultVMServiceFile,
	}
	sel.HasFlavor = len(flavors) > 0 // treat the hardcoded default as "set"

	if v, ok := os.LookupEnv("FLUTTER_FLAVOR"); ok {
		sel.Flavor = v
		sel.HasFlavor = true
	}
	if v, ok := os.LookupEnv("FLUTTER_VMSERVICE_FILE"); ok {
		sel.VMServiceFile = v
	}
	statePath := os.Getenv("FRN_STATE_FILE")
	if statePath == "" {
		statePath = ".dart_tool/valian/frn.json"
	}
	saved, _ := state.Load(statePath)
	if saved.HasFlavor {
		sel.Flavor = saved.Flavor
		sel.HasFlavor = true
	}
	if saved.VMServiceFile != "" {
		sel.VMServiceFile = saved.VMServiceFile
	}
	savedDeviceID := saved.DeviceID

	if o.cliFlavor != nil {
		sel.Flavor = *o.cliFlavor
		sel.HasFlavor = true
	}
	if o.cliVMService != "" {
		sel.VMServiceFile = o.cliVMService
	}

	probe := device.ProbeAll(ctx)
	out, err := ui.Run(ctx, ui.Input{
		InitialSelection: sel,
		InitialProbe:     probe,
		SavedDeviceID:    savedDeviceID,
		AvailableFlavors: flavors,
		ExtraArgs:        o.extraArgs,
	})
	if err != nil {
		return err
	}
	sel = out.Selection

	if err := os.MkdirAll(filepath.Dir(sel.VMServiceFile), 0o755); err != nil {
		return err
	}

	// Best-effort state save — a failure here shouldn't prevent launching.
	_ = state.Save(statePath, state.State{
		Flavor:        sel.Flavor,
		DeviceID:      sel.Device.ID,
		DeviceLabel:   sel.Device.Label,
		VMServiceFile: sel.VMServiceFile,
	})

	flutterArgs := sel.FlutterArgs(o.extraArgs)
	fmt.Fprintf(os.Stderr, "→ flutter run %s\n", strings.Join(flutterArgs, " "))

	return execFlutter(flutterArgs)
}

func defaultFlavor(flavors []string) string {
	if len(flavors) > 0 {
		return "development"
	}
	return ""
}

// parseArgs implements the same option grammar as the bash version:
// --flavor X / --flavor=X / --no-flavor / --vmservice=X / -h / --help /
// --version, with pass-through after `--` and unknown args forwarded.
func parseArgs(argv []string) (opts, error) {
	var o opts
	i := 0
	for i < len(argv) {
		a := argv[i]
		switch {
		case a == "-h" || a == "--help":
			o.showHelp = true
			return o, nil
		case a == "--version":
			o.showVersion = true
			return o, nil
		case a == "--flavor":
			if i+1 >= len(argv) {
				return o, fmt.Errorf("--flavor needs a value")
			}
			v := argv[i+1]
			o.cliFlavor = &v
			i += 2
		case strings.HasPrefix(a, "--flavor="):
			v := strings.TrimPrefix(a, "--flavor=")
			o.cliFlavor = &v
			i++
		case a == "--no-flavor":
			empty := ""
			o.cliFlavor = &empty
			i++
		case strings.HasPrefix(a, "--vmservice="):
			o.cliVMService = strings.TrimPrefix(a, "--vmservice=")
			i++
		case a == "--":
			o.extraArgs = append(o.extraArgs, argv[i+1:]...)
			return o, nil
		default:
			o.extraArgs = append(o.extraArgs, a)
			i++
		}
	}
	return o, nil
}

// execFlutter replaces the current process so flutter run inherits our
// terminal + signals cleanly, exactly like the bash `exec flutter run`.
func execFlutter(args []string) error {
	path, err := exec.LookPath("flutter")
	if err != nil {
		return fmt.Errorf("flutter not in PATH")
	}
	full := append([]string{"flutter", "run"}, args...)
	return syscall.Exec(path, full, os.Environ())
}

type defaults struct {
	flavor   string
	vmservice string
}

func detectDefaults() defaults {
	flavors := flavor.Detect(".")
	d := defaults{vmservice: defaultVMServiceFile}
	if v, ok := os.LookupEnv("FLUTTER_VMSERVICE_FILE"); ok {
		d.vmservice = v
	}
	d.flavor = defaultFlavor(flavors)
	if v, ok := os.LookupEnv("FLUTTER_FLAVOR"); ok {
		d.flavor = v
	}
	return d
}

func printUsage(w *os.File, d defaults) {
	flavorLine := d.flavor
	if flavorLine == "" {
		flavorLine = "(none)"
	}
	fmt.Fprintf(w, `frn — flutter run, with a fast device picker.

USAGE
  frn [options] [-- args forwarded to flutter run]

OPTIONS
  --flavor <name>       Flavor to use (default: %s).
  --no-flavor           Run without --flavor (some projects don't use flavors).
  --vmservice=<path>    Path for --vmservice-out-file
                        (default: %s).
  --version             Print version and exit.
  -h, --help            Show this help.

  Anything else is forwarded as-is to `+"`flutter run`"+`. Use `+"`--`"+`
  to avoid ambiguity:
    frn -- --dart-define=FOO=1 --release

ENVIRONMENT
  FLUTTER_FLAVOR           Override the default flavor.
  FLUTTER_VMSERVICE_FILE   Override the vmservice output file.
  FRN_STATE_FILE           Path to the state file
                           (default: .dart_tool/valian/frn.json).

STATE
  Each successful launch writes flavor, device, and vmservice path to
  the state file. On next run, those values are loaded as defaults; the
  saved device is reused if it's still connected, otherwise the picker
  is shown. Delete the state file to reset.

DEPENDENCIES
  adb, xcrun (depending on which platforms you target).

EXAMPLES
  frn                              # development, device menu
  frn --flavor staging             # override flavor
  frn -d 2B021FDH3005LU            # skip the menu, device already known
  frn -- --dart-define=API_URL=…   # extras forwarded to flutter run
`, flavorLine, d.vmservice)
}
