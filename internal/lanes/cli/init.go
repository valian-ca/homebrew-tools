package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/valian-ca/homebrew-tools/internal/lanes/adopt"
)

const initTimeout = time.Minute

func newInitCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a project's lanes files from what it detects, and report what it could not infer",
		Long: `Write the lanes files of the project at the root of the current checkout —
the main checkout or a worktree — from what it detects, then print a report.
It asks nothing, and needs neither lanesd nor process-compose.

It writes lanes.yaml (schema 2) and process-compose.yaml, and refuses, naming
the file and writing nothing, when either exists. It adds the contract's four
guarded hooks to .claude/settings.json, keeping everything else in the file;
the session-start and cwd-changed hooks get a 600 s timeout when lanes.yaml
has setup steps. It appends the lanes line to .envrc and, with a firebase
resource, the generated configs and the data directory to .gitignore. Each
file is created when missing; a hook or a line already there is not added
twice.

It detects:
  Firebase      a firebase.emulator.json, else firebase.json, with an emulators
                section: a firebase resource and every port the contract
                requires, for the stack and its test series
  MySQL         a compose service on a mysql or mariadb image: a mysql resource
                with its host port, root password, a docker compose start and
                listeners [com.docker.backend]
  Postgres      a compose service on a postgres image: reported, not written
  Dev servers   package.json scripts running vite, vite dev, vite serve,
                storybook dev or start-storybook: a disabled process each, on
                its own named port, started with lanes up <process>
  Setup         pnpm-lock.yaml, package-lock.json, yarn.lock and pubspec.lock:
                an install step each, rerun when the lockfile changes

It never infers, and always reports: the MySQL databases, a seed, a scripts
resource, the generate templates for the files that carry ports today, the
project's own processes, and a MySQL server not run from docker-compose. Each
is a "# lanes init:" comment where to fill it in. The report also lists what
looks like a former worktree harness in .envrc and .claude/settings.json; it
removes none of it.

The contract: lanes help contract`,
		Example: `  lanes init --dry-run    # print the files and the report, write nothing
  lanes init              # write them, then fill in the reported items`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := cwd()
			if err != nil {
				return err
			}
			ctx, cancel := withTimeout(cmd.Context(), initTimeout)
			defer cancel()
			res, err := adopt.Plan(ctx, dir)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if dryRun {
				res.WriteDryRun(out)
				res.WriteReport(out, true)
				return nil
			}
			if err := res.Apply(); err != nil {
				return err
			}
			res.WriteReport(out, false)
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the files lanes init would write and merge, and the report; write nothing")
	return cmd
}
