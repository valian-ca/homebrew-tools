package cmds

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/atelierd/forge"
)

func NewForgeCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "forge",
		Short: "Manage autonomous forge run state",
		Example: `  atelierd forge run start VAL-306 --session session-abc123 --cap 4
  atelierd forge campaign save --run 01J00000000000000000000000 --from /tmp/campaign.json
  atelierd forge wave open --run 01J00000000000000000000000
  atelierd forge pass next --run 01J00000000000000000000000 --kind wave
  atelierd forge outcome record --run 01J00000000000000000000000 --pass wave-1 --from /tmp/outcome.json
  atelierd forge wave close --run 01J00000000000000000000000 --findings 0
  atelierd forge summary --run 01J00000000000000000000000

  atelierd forge pass next --run 01J00000000000000000000000 --kind review
  atelierd forge outcome record --run 01J00000000000000000000000 --pass review-1 --from /tmp/review-outcome.json
  atelierd forge ref set https://linear.app/valian/issue/VAL-306#comment-1 --run 01J00000000000000000000000 --key report
  atelierd forge testplan render --run 01J00000000000000000000000 --lang en --out /tmp/testplan.md`,
	}
	command.AddCommand(
		newForgeRunCmd(),
		newForgeWaveCmd(),
		newForgePassCmd(),
		newForgeCampaignCmd(),
		newForgeOutcomeCmd(),
		newForgeSummaryCmd(),
		newForgeRefCmd(),
		newForgeTestplanCmd(),
		newForgeContractCmd(),
	)
	return command
}

func newForgeRunCmd() *cobra.Command {
	group := &cobra.Command{Use: "run", Short: "Start and inspect forge runs"}
	var session string
	var cap int
	start := &cobra.Command{
		Use:   "start <ticket> --session <id> [--cap N]",
		Short: "Start a fresh isolated run and print its ULID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runID, err := forge.StartContext(cmd.Context(), args[0], session, cap)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), runID)
			return nil
		},
	}
	start.Flags().StringVar(&session, "session", "", "Claude session ID persisted for all run events (required)")
	start.Flags().IntVar(&cap, "cap", forge.DefaultCap, "maximum number of normal waves")
	_ = start.MarkFlagRequired("session")
	var runID string
	status := &cobra.Command{
		Use:   "status --run <id>",
		Short: "Print one compact JSON line with wave, open pass, and refs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			value, err := forge.StatusJSONContext(cmd.Context(), runID)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(value))
			return nil
		},
	}
	status.Flags().StringVar(&runID, "run", "", "forge run ULID (required)")
	_ = status.MarkFlagRequired("run")
	group.AddCommand(start, status)
	return group
}

func newForgeWaveCmd() *cobra.Command {
	group := &cobra.Command{Use: "wave", Short: "Open and close normal verification waves"}
	var openRun string
	open := &cobra.Command{
		Use:   "open --run <id>",
		Short: "Open the next wave and print its number",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			wave, err := forge.OpenWaveContext(cmd.Context(), openRun)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), wave)
			return nil
		},
	}
	open.Flags().StringVar(&openRun, "run", "", "forge run ULID (required)")
	_ = open.MarkFlagRequired("run")
	var closeRun string
	var findings int
	close := &cobra.Command{
		Use:   "close --run <id> --findings <n>",
		Short: "Close the open wave and print continue, dry, or cap",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			decision, err := forge.CloseWaveContext(cmd.Context(), closeRun, findings)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), decision)
			return nil
		},
	}
	close.Flags().StringVar(&closeRun, "run", "", "forge run ULID (required)")
	close.Flags().IntVar(&findings, "findings", 0, "number of findings in the wave (required)")
	_ = close.MarkFlagRequired("run")
	_ = close.MarkFlagRequired("findings")
	group.AddCommand(open, close)
	return group
}

func newForgePassCmd() *cobra.Command {
	group := &cobra.Command{Use: "pass", Short: "Allocate isolated probe passes"}
	var runID, kind string
	next := &cobra.Command{
		Use:   "next --run <id> --kind wave|review|repair",
		Short: "Allocate a pass and print its absolute captures directory",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := forge.NextPassContext(cmd.Context(), runID, kind)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), path)
			return nil
		},
	}
	next.Flags().StringVar(&runID, "run", "", "forge run ULID (required)")
	next.Flags().StringVar(&kind, "kind", "", "pass kind: wave, review, or repair (required)")
	_ = next.MarkFlagRequired("run")
	_ = next.MarkFlagRequired("kind")
	group.AddCommand(next)
	return group
}

func newForgeCampaignCmd() *cobra.Command {
	group := &cobra.Command{Use: "campaign", Short: "Store and load the verification campaign"}
	var saveRun, staging string
	save := &cobra.Command{
		Use:   "save --run <id> --from <staging.json>",
		Short: "Validate and atomically store a campaign",
		Long: `Validate and atomically store this JSON shape:
{"schemaVersion":1,"axes":[{"title":"Axis","scenarios":[{"title":"Scenario","steps":["Step"],"expected":"Result"}]}]}`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return forge.SaveCampaignContext(cmd.Context(), saveRun, staging)
		},
	}
	save.Flags().StringVar(&saveRun, "run", "", "forge run ULID (required)")
	save.Flags().StringVar(&staging, "from", "", "campaign staging JSON file (required)")
	_ = save.MarkFlagRequired("run")
	_ = save.MarkFlagRequired("from")
	var loadRun string
	load := &cobra.Command{
		Use:   "load --run <id>",
		Short: "Print the stored campaign as multi-line JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			value, err := forge.LoadCampaignContext(cmd.Context(), loadRun)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(value)
			return err
		},
	}
	load.Flags().StringVar(&loadRun, "run", "", "forge run ULID (required)")
	_ = load.MarkFlagRequired("run")
	group.AddCommand(save, load)
	return group
}

func newForgeOutcomeCmd() *cobra.Command {
	group := &cobra.Command{Use: "outcome", Short: "Append validated pass outcomes"}
	var runID, passID, staging string
	record := &cobra.Command{
		Use:   "record --run <id> --pass <pass-id> --from <staging.json>",
		Short: "Validate outcomes and append computed per-pass counts",
		Long: `Validate and append this JSON shape:
{"schemaVersion":1,"outcomes":[{"axis":"Axis","scenario":"Scenario","status":"pass|finding|not_exercised","reason":"optional"}]}

The command computes and persists pass, finding, and not_exercised counts; staging files never supply counts.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return forge.RecordOutcomeContext(cmd.Context(), runID, passID, staging)
		},
	}
	record.Flags().StringVar(&runID, "run", "", "forge run ULID (required)")
	record.Flags().StringVar(&passID, "pass", "", "allocated pass ID, such as wave-1 or review-1 (required)")
	record.Flags().StringVar(&staging, "from", "", "outcome staging JSON file (required)")
	_ = record.MarkFlagRequired("run")
	_ = record.MarkFlagRequired("pass")
	_ = record.MarkFlagRequired("from")
	group.AddCommand(record)
	return group
}

func newForgeSummaryCmd() *cobra.Command {
	var runID string
	command := &cobra.Command{
		Use:   "summary --run <id>",
		Short: "Print deterministic paste-ready Markdown counts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			value, err := forge.SummaryContext(cmd.Context(), runID)
			if err != nil {
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), value)
			return nil
		},
	}
	command.Flags().StringVar(&runID, "run", "", "forge run ULID (required)")
	_ = command.MarkFlagRequired("run")
	return command
}

func newForgeRefCmd() *cobra.Command {
	group := &cobra.Command{Use: "ref", Short: "Store and retrieve Linear references"}
	var setRun, setKey string
	set := &cobra.Command{
		Use:   "set <value> --run <id> --key report|testplan",
		Short: "Store a Linear report comment or test plan document reference",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return forge.SetRefContext(cmd.Context(), setRun, setKey, args[0])
		},
	}
	set.Flags().StringVar(&setRun, "run", "", "forge run ULID (required)")
	set.Flags().StringVar(&setKey, "key", "", "reference key: report or testplan (required)")
	_ = set.MarkFlagRequired("run")
	_ = set.MarkFlagRequired("key")
	var getRun, getKey string
	get := &cobra.Command{
		Use:   "get --run <id> --key report|testplan",
		Short: "Print a stored Linear reference as one value",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			value, err := forge.GetRefContext(cmd.Context(), getRun, getKey)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), value)
			return nil
		},
	}
	get.Flags().StringVar(&getRun, "run", "", "forge run ULID (required)")
	get.Flags().StringVar(&getKey, "key", "", "reference key: report or testplan (required)")
	_ = get.MarkFlagRequired("run")
	_ = get.MarkFlagRequired("key")
	group.AddCommand(set, get)
	return group
}

func newForgeTestplanCmd() *cobra.Command {
	group := &cobra.Command{Use: "testplan", Short: "Render campaign and outcome evidence"}
	var runID, language, output string
	render := &cobra.Command{
		Use:   "render --run <id> --lang fr|en [--out file]",
		Short: "Render the test plan in French or English",
		Long: `Render every campaign axis and scenario with its latest real outcome.
Unsupported languages fall back to English with a warning. Without --out,
Markdown is written to stdout. With --out, the file is replaced atomically and
stdout contains only the path exactly as passed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected := language
			if selected != "fr" && selected != "en" {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: unsupported language %q; falling back to en\n", selected)
				selected = "en"
			}
			content, writtenPath, err := forge.RenderTestplanContext(cmd.Context(), runID, selected, output)
			if err != nil {
				return err
			}
			if writtenPath != "" {
				fmt.Fprintln(cmd.OutOrStdout(), writtenPath)
			} else {
				fmt.Fprint(cmd.OutOrStdout(), content)
			}
			return nil
		},
	}
	render.Flags().StringVar(&runID, "run", "", "forge run ULID (required)")
	render.Flags().StringVar(&language, "lang", "", "fixed label language: fr or en (required)")
	render.Flags().StringVar(&output, "out", "", "atomically write Markdown to this file")
	_ = render.MarkFlagRequired("run")
	_ = render.MarkFlagRequired("lang")
	group.AddCommand(render)
	return group
}

func newForgeContractCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "contract",
		Short: "Print the forge CLI contract version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), forge.ContractVersion)
		},
	}
}
