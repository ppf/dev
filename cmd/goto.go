package cmd

import (
	"strconv"

	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/sidebar"
	"github.com/ppf/dev/internal/tmux"
)

func newGotoCmd() *cobra.Command {
	var client string
	cmd := &cobra.Command{
		Use:   "goto <n>",
		Short: "Switch to the n-th session in sidebar order (prefix+1..9)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			n, err := strconv.Atoi(args[0])
			if err != nil {
				return err
			}
			return sidebar.Goto(tmux.New(), n, client)
		},
	}
	cmd.Flags().StringVar(&client, "client", "", "client tty to switch (default: tmux's current client)")
	return cmd
}

func newNewCmd() *cobra.Command {
	var client string
	cmd := &cobra.Command{
		Use:   "new",
		Short: "Open the project picker in a popup to open a project's boss (prefix+0)",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return sidebar.OpenPicker(tmux.New(), client)
		},
	}
	cmd.Flags().StringVar(&client, "client", "", "client tty to show the popup on (default: tmux's current client)")
	return cmd
}
