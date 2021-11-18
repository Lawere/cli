package extension

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/git"
	"github.com/cli/cli/v2/internal/ghrepo"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/extensions"
	"github.com/cli/cli/v2/pkg/prompt"
	"github.com/cli/cli/v2/utils"
	"github.com/spf13/cobra"
)

func NewCmdExtension(f *cmdutil.Factory) *cobra.Command {
	m := f.ExtensionManager
	io := f.IOStreams

	extCmd := cobra.Command{
		Use:   "extension",
		Short: "Manage gh extensions",
		Long: heredoc.Docf(`
			GitHub CLI extensions are repositories that provide additional gh commands.

			The name of the extension repository must start with "gh-" and it must contain an
			executable of the same name. All arguments passed to the %[1]sgh <extname>%[1]s invocation
			will be forwarded to the %[1]sgh-<extname>%[1]s executable of the extension.

			An extension cannot override any of the core gh commands.

			See the list of available extensions at <https://github.com/topics/gh-extension>
		`, "`"),
		Aliases: []string{"extensions"},
	}

	extCmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List installed extension commands",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				cmds := m.List(true)
				if len(cmds) == 0 {
					return errors.New("no extensions installed")
				}
				cs := io.ColorScheme()
				t := utils.NewTablePrinter(io)
				for _, c := range cmds {
					var repo string
					if u, err := git.ParseURL(c.URL()); err == nil {
						if r, err := ghrepo.FromURL(u); err == nil {
							repo = ghrepo.FullName(r)
						}
					}

					t.AddField(fmt.Sprintf("gh %s", c.Name()), nil, nil)
					t.AddField(repo, nil, nil)
					var updateAvailable string
					if c.UpdateAvailable() {
						updateAvailable = "Upgrade available"
					}
					t.AddField(updateAvailable, nil, cs.Green)
					t.EndRow()
				}
				return t.Render()
			},
		},
		&cobra.Command{
			Use:   "install <repository>",
			Short: "Install a gh extension from a repository",
			Long: heredoc.Doc(`
				Install a GitHub repository locally as a GitHub CLI extension.
				
				The repository argument can be specified in "owner/repo" format as well as a full URL.
				The URL format is useful when the repository is not hosted on github.com.
				
				To install an extension in development from the current directory, use "." as the
				value of the repository argument.

				See the list of available extensions at <https://github.com/topics/gh-extension>
			`),
			Example: heredoc.Doc(`
				$ gh extension install owner/gh-extension
				$ gh extension install https://git.example.com/owner/gh-extension
				$ gh extension install .
			`),
			Args: cmdutil.MinimumArgs(1, "must specify a repository to install from"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if args[0] == "." {
					wd, err := os.Getwd()
					if err != nil {
						return err
					}
					return m.InstallLocal(wd)
				}

				repo, err := ghrepo.FromFullName(args[0])
				if err != nil {
					return err
				}

				if err := checkValidExtension(cmd.Root(), m, repo.RepoName()); err != nil {
					return err
				}

				return m.Install(repo)
			},
		},
		func() *cobra.Command {
			var flagAll bool
			var flagForce bool
			cmd := &cobra.Command{
				Use:   "upgrade {<name> | --all}",
				Short: "Upgrade installed extensions",
				Args: func(cmd *cobra.Command, args []string) error {
					if len(args) == 0 && !flagAll {
						return cmdutil.FlagErrorf("must specify an extension to upgrade")
					}
					if len(args) > 0 && flagAll {
						return cmdutil.FlagErrorf("cannot use `--all` with extension name")
					}
					if len(args) > 1 {
						return cmdutil.FlagErrorf("too many arguments")
					}
					return nil
				},
				RunE: func(cmd *cobra.Command, args []string) error {
					var name string
					if len(args) > 0 {
						name = normalizeExtensionSelector(args[0])
					}
					cs := io.ColorScheme()
					err := m.Upgrade(name, flagForce)
					if err != nil {
						if name != "" {
							fmt.Fprintf(io.ErrOut, "%s Failed upgrading extension %s: %s", cs.FailureIcon(), name, err)
						} else {
							fmt.Fprintf(io.ErrOut, "%s Failed upgrading extensions", cs.FailureIcon())
						}
						return cmdutil.SilentError
					}
					if io.IsStdoutTTY() {
						if name != "" {
							fmt.Fprintf(io.Out, "%s Successfully upgraded extension %s\n", cs.SuccessIcon(), name)
						} else {
							fmt.Fprintf(io.Out, "%s Successfully upgraded extensions\n", cs.SuccessIcon())
						}
					}
					return nil
				},
			}
			cmd.Flags().BoolVar(&flagAll, "all", false, "Upgrade all extensions")
			cmd.Flags().BoolVar(&flagForce, "force", false, "Force upgrade extension")
			return cmd
		}(),
		&cobra.Command{
			Use:   "remove <name> [--precompiled]",
			Short: "Remove an installed extension",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				extName := normalizeExtensionSelector(args[0])
				if err := m.Remove(extName); err != nil {
					return err
				}
				if io.IsStdoutTTY() {
					cs := io.ColorScheme()
					fmt.Fprintf(io.Out, "%s Removed extension %s\n", cs.SuccessIcon(), extName)
				}
				return nil
			},
		},
		func() *cobra.Command {
			promptCreate := func() (string, extensions.ExtTemplateType, error) {
				var extName string
				var extTmplType int
				err := prompt.SurveyAskOne(&survey.Input{
					Message: "Extension name:",
				}, &extName)
				if err != nil {
					return extName, -1, err
				}
				err = prompt.SurveyAskOne(&survey.Select{
					Message: "What kind of extension?",
					Options: []string{
						"Script (Bash, Ruby, Python, etc)",
						"Go",
						"Other Precompiled (C++, Rust, etc)",
					},
				}, &extTmplType)
				return extName, extensions.ExtTemplateType(extTmplType), err
			}
			// TODO fix flags
			var flagGoBinary bool
			var flagOtherBinary bool
			cmd := &cobra.Command{
				Use:   "create [name]",
				Short: "Create a new extension",
				Example: heredoc.Doc(`
				# Use interactively
				gh extension create

				# Create a script-based extension
				gh extension create foobar

				# Create a Go extension
				gh extension create --precompiled-go foobar

				# Create a non-Go precompiled extension
				gh extension create --precompiled-other foobar
				`),
				Args: cobra.MaximumNArgs(1),
				RunE: func(cmd *cobra.Command, args []string) error {
					if err := cmdutil.MutuallyExclusive("specify only one of --precompiled-go and --precompiled-other", flagGoBinary, flagOtherBinary); err != nil {
						return err
					}
					var extName string
					var err error
					tmplType := extensions.GitTemplateType
					if len(args) == 0 {
						if io.IsStdoutTTY() {
							extName, tmplType, err = promptCreate()
							if err != nil {
								return fmt.Errorf("could not prompt: %w", err)
							}
						}
					} else {
						extName = args[0]
						if flagGoBinary {
							tmplType = extensions.GoBinTemplateType
						} else if flagOtherBinary {
							tmplType = extensions.OtherBinTemplateType
						}
					}

					if !strings.HasPrefix(extName, "gh-") {
						extName = "gh-" + extName
					}
					if err := m.Create(extName, tmplType); err != nil {
						return err
					}
					if !io.IsStdoutTTY() {
						return nil
					}
					binaryNote := ""
					if tmplType == extensions.GoBinTemplateType {
						binaryNote = heredoc.Docf(`

							Remember to run 'go build -o %s' to see changes.
						`, extName)
					} else if tmplType == extensions.OtherBinTemplateType {
						binaryNote = heredoc.Docf(`

							Remember to:
							  - fill in script/build.sh with your compilation script for automated builds.
							  - compile a %[1]s binary locally to see changes.
						`, extName)
					}
					link := "https://docs.github.com/github-cli/github-cli/creating-github-cli-extensions"
					cs := io.ColorScheme()
					out := heredoc.Docf(`
					%[1]s Created directory %[2]s
					%[1]s Initialized git repository
					%[1]s Set up extension scaffolding

					%[2]s is ready for development

					Install locally with: cd %[2]s && gh extension install .
					%[4]s
					Publish to GitHub with: gh repo create %[2]s

					For more information on writing extensions:
					%[3]s
				`, cs.SuccessIcon(), extName, link, binaryNote)
					fmt.Fprint(io.Out, out)
					return nil
				},
			}
			cmd.Flags().BoolVar(&flagGoBinary, "precompiled-go", false, "Create a precompiled Go extension")
			cmd.Flags().BoolVar(&flagOtherBinary, "precompiled-other", false, "Create a precompiled non-Go extension")
			return cmd
		}(),
	)

	return &extCmd
}

func checkValidExtension(rootCmd *cobra.Command, m extensions.ExtensionManager, extName string) error {
	if !strings.HasPrefix(extName, "gh-") {
		return errors.New("extension repository name must start with `gh-`")
	}

	commandName := strings.TrimPrefix(extName, "gh-")
	if c, _, err := rootCmd.Traverse([]string{commandName}); err != nil {
		return err
	} else if c != rootCmd {
		return fmt.Errorf("%q matches the name of a built-in command", commandName)
	}

	for _, ext := range m.List(false) {
		if ext.Name() == commandName {
			return fmt.Errorf("there is already an installed extension that provides the %q command", commandName)
		}
	}

	return nil
}

func normalizeExtensionSelector(n string) string {
	if idx := strings.IndexRune(n, '/'); idx >= 0 {
		n = n[idx+1:]
	}
	return strings.TrimPrefix(n, "gh-")
}
