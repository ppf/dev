package cmd

import (
	"github.com/spf13/cobra"

	"github.com/ppf/dev/internal/compact"
	"github.com/ppf/dev/internal/tmux"
)

func newCompactCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compact <client>",
		Short: "client-* hooks: zoom dev panes for a narrow client, unzoom when wide",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			client := ""
			if len(args) == 1 {
				client = args[0]
			}
			return shown(compact.Run(tmux.New(), client))
		},
	}
}
