package cli

import (
	"cmp"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/goxang/rig/engine"
	"github.com/goxang/rig/internal/kubectx"
)

func kubeconfigCommand() *cobra.Command {
	var token string
	var insecure bool
	c := &cobra.Command{
		Use:   "kubeconfig [url | file | -]",
		Short: "fetch the environment's kubeconfig and merge it into yours (~/.kube/config, the old one kept as .rig-backup)",
		Example: `  rig -e loadtest2 kubeconfig                   # a Rancher server: asks for an API key ($RANCHER_TOKEN)
  rig -e k8s kubeconfig https://host/kubeconfig  # downloads it (--token sends a bearer token)
  rig -e k8s kubeconfig ~/Downloads/cluster.yaml
  pbpaste | rig -e k8s kubeconfig -`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			env, ctxName, server, err := engine.KubeTarget(g.file, g.env)
			if err != nil {
				return err
			}
			source := ""
			if len(args) > 0 {
				source = args[0]
			}
			var raw []byte
			if source == "-" {
				if raw, err = io.ReadAll(os.Stdin); err != nil {
					return err
				}
			} else {
				token = cmp.Or(token, os.Getenv("RANCHER_TOKEN"))
				if source == "" && token == "" && kubectx.IsRancher(server) {
					if token, err = readSecret("Rancher API key for " + server); err != nil {
						return err
					}
				}
				if raw, err = kubectx.Fetch(cmd.Context(), source, server, strings.TrimSpace(token), insecure); err != nil {
					return err
				}
			}
			file, names, err := kubectx.Merge(raw)
			if err != nil {
				return err
			}
			fmt.Printf("%s %s: contexts %s\n", green("✓"), file, strings.Join(names, ", "))
			use, err := kubectx.Resolve(ctxName, server)
			if err != nil {
				return fmt.Errorf("merged, but %s still finds no context: %w", env, err)
			}
			fmt.Printf("%s uses context %s\n", env, use)
			return nil
		},
	}
	c.Flags().StringVar(&token, "token", "", "bearer token for the download (Rancher: an API key; default $RANCHER_TOKEN)")
	c.Flags().BoolVar(&insecure, "insecure", false, "skip TLS verification of the download (a self-signed Rancher)")
	return c
}
