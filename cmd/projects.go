package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/config"
)

func newListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Print projects as name<TAB>description<TAB>path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			projects, _, err := loadProjects(cmd)
			if err != nil {
				return err
			}
			for _, p := range projects {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", p.Name, p.Description, p.Path)
			}
			return nil
		},
	}
}

func newImportCmd() *cobra.Command {
	var from string
	cmd := &cobra.Command{
		Use:   "import-projects",
		Short: "Convert ~/.tmux/sessions/projects.conf to projects.toml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if from == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				from = filepath.Join(home, ".tmux/sessions/projects.conf")
			}
			to, err := config.DefaultPath()
			if err != nil {
				return err
			}
			n, err := config.Import(from, to, reservedNames(cmd))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Imported %d projects into %s\n", n, to)
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "projects.conf to import (default ~/.tmux/sessions/projects.conf)")
	return cmd
}
