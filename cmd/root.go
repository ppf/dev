package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/config"
	"github.com/ppf/dev/internal/open"
	"github.com/ppf/dev/internal/picker"
	"github.com/ppf/dev/internal/tmux"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:               "dev [project]",
		Short:             "tmux workspace launcher for Claude Code agents",
		Args:              cobra.ArbitraryArgs,
		SilenceUsage:      true,
		SilenceErrors:     true,
		CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	}
	root.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return runPicker(cmd)
		}
		return runProject(cmd, args[0])
	}
	root.AddCommand(newListCmd(), newImportCmd(), newStateCmd(), newSidebarCmd(), newGotoCmd(), newNewCmd())
	return root
}

// Execute runs dev; errors are printed as "❌ <err>" (unless already shown) and exit 1.
func Execute() {
	err := newRootCmd().Execute()
	if err == nil {
		return
	}
	if !errors.Is(err, open.ErrShown) {
		fmt.Fprintln(os.Stderr, "❌ "+err.Error())
		// The popup closes as soon as dev exits, taking the message with it.
		if os.Getenv(picker.PopupGuard) != "" {
			open.Pause(os.Stderr, "Press enter to close…")
		}
	}
	os.Exit(1)
}

func runPicker(cmd *cobra.Command) error {
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
	return open.Run(tmux.New(), open.Options{
		Path: dir, Name: res.Project.Name, Worktree: res.Worktree, ForceNew: true,
	}, cmd.OutOrStdout())
}

func runProject(cmd *cobra.Command, name string) error {
	projects, path, err := loadProjects(cmd)
	if err != nil {
		return err
	}
	p, ok := config.Find(projects, name)
	if !ok {
		stderr := cmd.ErrOrStderr()
		fmt.Fprintf(stderr, "❌ Project '%s' not found in %s\nAvailable:\n", name, path)
		for _, p := range projects {
			fmt.Fprintln(stderr, "  "+p.Name)
		}
		return fmt.Errorf("%w: project %q not found", open.ErrShown, name)
	}
	dir, err := p.Dir()
	if err != nil {
		return err
	}
	return open.Run(tmux.New(), open.Options{Path: dir, Name: p.Name}, cmd.OutOrStdout())
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

// reservedNames are the names `dev <name>` can't reach: every subcommand and alias, and help.
func reservedNames(cmd *cobra.Command) []string {
	reserved := []string{"help"}
	for _, c := range cmd.Root().Commands() {
		reserved = append(reserved, c.Name())
		reserved = append(reserved, c.Aliases...)
	}
	return reserved
}
