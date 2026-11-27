package cmd

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/agent"
	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/herd"
	"github.com/ppf/dev/internal/inbox"
	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/picker"
	"github.com/ppf/dev/internal/selfreload"
	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
	"github.com/ppf/dev/internal/worktree"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:               "dev [project [worktree]]",
		Short:             "tmux workspace launcher for Claude Code agents",
		Args:              cobra.MaximumNArgs(2),
		SilenceUsage:      true,
		SilenceErrors:     true,
		ValidArgsFunction: completeArgs,
		Version:           selfreload.Version(),
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return runPicker(cmd, agent.Start{})
		}
		wt, err := worktreeArg(args)
		if err != nil {
			return err
		}
		return openProject(cmd, args[0], wt, false, false, "", "", agent.Start{})
	}
	root.AddCommand(newListCmd(), newImportCmd(), newStateCmd(), newSidebarCmd(), newGotoCmd(), newNewCmd(),
		newResurrectCmd(), newCompactCmd(), newAgentCmd(), newStatuslineCmd(), newHerdCmd(), newPtyWrapCmd(), newQuestionCmd(),
		newDeliveredCmd(), newDesktopCmd(), newTodoCmd(), newServeCmd(), newAgyDialogCmd())
	return root
}

// worktreeArg is the optional second argument of `dev <project> [worktree]`: "" when absent.
func worktreeArg(args []string) (string, error) {
	if len(args) < 2 {
		return "", nil
	}
	if args[1] == "" {
		return "", errors.New("empty worktree name")
	}
	return args[1], nil
}

// Execute runs dev; errors are printed as "❌ <err>" (unless already shown) and exit with exitCode.
func Execute() {
	// Every close of a boss, whichever path, writes its last-session file
	sidebar.BossEnding = inbox.BossClosing
	err := newRootCmd().Execute()
	if err == nil {
		return
	}
	if !errors.Is(err, open.ErrShown) {
		fmt.Fprintln(os.Stderr, "❌ "+err.Error())
		// The popup closes as soon as dev exits, taking the message with it.
		if os.Getenv(picker.PopupGuard) != "" || os.Getenv(showGuard) != "" || os.Getenv(boardGuard) != "" {
			open.Pause(os.Stderr, "Press enter to close…")
		}
	}
	os.Exit(exitCode(err))
}

// runPicker opens the chosen project's boss, or with ctrl-o its plain session, either one's Claude with start.
func runPicker(cmd *cobra.Command, start agent.Start) error {
	if os.Getenv("TMUX") != "" && os.Getenv(picker.PopupGuard) == "" {
		code, err := tmux.New().ReexecInPopup(picker.PopupGuard, "80%", "60%")
		if err != nil {
			return err
		}
		if code != 0 {
			os.Exit(code)
		}
		return nil
	}
	projects, _, err := loadProjects(cmd)
	if err != nil {
		return err
	}
	res, err := picker.Run(projects)
	if err != nil || !res.Chosen {
		return err
	}
	dir, err := res.Project.Dir()
	if err != nil {
		return err
	}
	c := tmux.New()
	if res.Session {
		return open.Run(c, open.Options{Path: dir, Name: res.Project.Name, Start: start}, cmd.OutOrStdout())
	}
	if err := open.RestoreOnce(c, os.Getenv("HOME"), cmd.OutOrStdout()); err != nil {
		return err
	}
	return herd.Open(c, res.Project.Name, dir, start)
}

// openProject opens project name, or its worktree wt (created when missing) when wt is set; detach
// creates the session without attaching and sends messages to stderr, keeping stdout for results;
// agentCmd starts in a new session's Agent pane ("" = claude), a claude with start's model and effort.
func openProject(cmd *cobra.Command, name, wt string, detach, worker bool, agentCmd, boss string, start agent.Start) error {
	// Rejected rather than sanitized: a typo must not become a different branch.
	if wt != "" && worktree.Sanitize(wt) != wt {
		return fmt.Errorf("invalid worktree name %q: only A-Za-z0-9._/- allowed", wt)
	}
	projects, path, err := loadProjects(cmd)
	if err != nil {
		return err
	}
	p, ok := config.Find(projects, name)
	if !ok {
		stderr := cmd.ErrOrStderr()
		fmt.Fprintf(stderr, "❌ Project '%s' not found in %s\n", name, path)
		printAvailable(stderr, projects)
		return fmt.Errorf("%w: project %q not found", open.ErrShown, name)
	}
	dir, err := p.Dir()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if detach {
		out = cmd.ErrOrStderr()
		// The worktree's git and rsync write to os.Stdout
		stdout := os.Stdout
		os.Stdout = os.Stderr
		defer func() { os.Stdout = stdout }()
	}
	return open.Run(tmux.New(), open.Options{Path: dir, Name: p.Name, Worktree: wt, Detach: detach, Agent: agentCmd, Worker: worker, Boss: boss, Start: start}, out)
}

// printAvailable lists the project names.
func printAvailable(w io.Writer, projects []config.Project) {
	fmt.Fprintln(w, "Available:")
	for _, p := range projects {
		fmt.Fprintln(w, "  "+p.Name)
	}
}

// completeArgs completes project names (with descriptions), then that project's worktree names.
func completeArgs(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	projects, _, err := loadProjects(cmd)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	if len(args) == 0 {
		names := make([]string, len(projects))
		for i, p := range projects {
			names[i] = p.Name
			if p.Description != "" {
				names[i] = cobra.CompletionWithDesc(p.Name, p.Description)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	p, ok := config.Find(projects, args[0])
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	dir, err := p.Dir()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	root, ok := worktree.Root(dir)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names, err := worktree.List(root)
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

// loadProjects reads projects.toml with every subcommand name reserved; also returns its path.
func loadProjects(cmd *cobra.Command) ([]config.Project, string, error) {
	path, err := config.DefaultPath()
	if err != nil {
		return nil, "", err
	}
	projects, err := config.Load(path, reservedNames(cmd))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", fmt.Errorf("%w (create it with: dev import-projects)", err)
	}
	return projects, path, err
}

// reservedNames are the names `dev <name>` can't reach, every subcommand and alias and help, and the manager's
// session name.
func reservedNames(cmd *cobra.Command) []string {
	reserved := []string{"help", herd.Manager}
	for _, c := range cmd.Root().Commands() {
		reserved = append(reserved, c.Name())
		reserved = append(reserved, c.Aliases...)
	}
	return reserved
}
