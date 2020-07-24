package operations

import (
	"context"
	"fmt"
	"strings"

	"github.com/evergreen-ci/evergreen/model"
	"github.com/evergreen-ci/evergreen/model/patch"
	"github.com/mongodb/grip"
	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

func PatchSetModule() cli.Command {
	const largeFlagName = "large"

	return cli.Command{
		Name:    "patch-set-module",
		Aliases: []string{"set-module"},
		Usage:   "update or add module to an existing patch",
		Flags: mergeFlagSlices(addPatchIDFlag(), addPathFlag(), addModuleFlag(), addYesFlag(), addRefFlag(), addUncommittedChangesFlag(), addPreserveCommitsFlag(
			cli.BoolFlag{
				Name:  largeFlagName,
				Usage: "enable submitting larger patches (>16MB)",
			})),
		Before: mergeBeforeFuncs(
			requirePatchIDFlag,
			requireModuleFlag,
			mutuallyExclusiveArgs(false, uncommittedChangesFlag, preserveCommitsFlag),
		),
		Action: func(c *cli.Context) error {
			confPath := c.Parent().String(confFlagName)
			module := c.String(moduleFlagName)
			patchID := c.String(patchIDFlagName)
			large := c.Bool(largeFlagName)
			skipConfirm := c.Bool(yesFlagName)
			project := c.String(projectFlagName)
			ref := c.String(refFlagName)
			uncommittedOk := c.Bool(uncommittedChangesFlag)
			preserveCommits := c.Bool(preserveCommitsFlag)
			args := c.Args()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			conf, err := NewClientSettings(confPath)
			if err != nil {
				return errors.Wrap(err, "problem loading configuration")
			}
			client := conf.setupRestCommunicator(ctx)
			defer client.Close()
			ac, rc, err := conf.getLegacyClients()
			if err != nil {
				return errors.Wrap(err, "problem accessing evergreen service")
			}

			keepGoing, err := confirmUncommittedChanges(preserveCommits, uncommittedOk || conf.UncommittedChanges)
			if err != nil {
				return errors.Wrap(err, "can't test for uncommitted changes")
			}
			if !keepGoing {
				return nil
			}

			proj, err := rc.GetPatchedConfig(patchID)
			if err != nil {
				return err
			}

			moduleBranch, err := getModuleBranch(module, proj)
			if err != nil {
				grip.Error(err)
				mods, merr := ac.GetPatchModules(patchID, proj.Identifier)
				if merr != nil {
					return errors.Wrap(merr, "errors fetching list of available modules")
				}

				if len(mods) != 0 {
					grip.Noticef("known modules includes:\n\t%s", strings.Join(mods, "\n\t"))
				}

				return errors.Errorf("could not set specified module: \"%s\"", module)
			}

			if uncommittedOk || conf.UncommittedChanges {
				ref = ""
			}

			// diff against the module branch.
			diffData, err := loadGitData(moduleBranch, ref, "", preserveCommits, args...)
			if err != nil {
				return err
			}
			if !preserveCommits {
				var existingPatch *patch.Patch
				if existingPatch, err = ac.GetPatch(patchID); err != nil {
					return errors.Wrapf(err, "can't get existing patch '%s'", patchID)
				}
				diffData.fullPatch, err = diffToMbox(diffData, existingPatch.Description)
				if err != nil {
					return err
				}
			}

			if err = validatePatchSize(diffData, large); err != nil {
				return err
			}

			if !skipConfirm {
				fmt.Printf("Using branch %v for module %v \n", moduleBranch, module)
				if diffData.patchSummary != "" {
					fmt.Println(diffData.patchSummary)
				}

				if !confirm("This is a summary of the patch to be submitted. Continue? (y/n):", true) {
					return nil
				}
			}

			params := UpdatePatchModuleParams{
				patchID: patchID,
				module:  module,
				patch:   diffData.fullPatch,
				base:    diffData.base,
			}
			err = ac.UpdatePatchModule(params)
			if err != nil {
				mods, err := ac.GetPatchModules(patchID, project)
				var msg string
				if err != nil {
					msg = fmt.Sprintf("could not find module named %s or retrieve list of modules",
						module)
				} else if len(mods) == 0 {
					msg = fmt.Sprintf("could not find modules for this project. %s is not a module. "+
						"see the evergreen configuration file for module configuration.",
						module)
				} else {
					msg = fmt.Sprintf("could not find module named '%s', select correct module from:\n\t%s",
						module, strings.Join(mods, "\n\t"))
				}
				grip.Error(msg)
				return err

			}
			fmt.Println("Module updated.")
			return nil
		},
	}
}

func PatchRemoveModule() cli.Command {
	return cli.Command{
		Name:    "patch-remove-module",
		Aliases: []string{"rm-module", "patch-rm-module"},
		Usage:   "remove a module from an existing patch",
		Flags:   mergeFlagSlices(addPatchIDFlag(), addPathFlag(), addModuleFlag()),
		Before:  mergeBeforeFuncs(requirePatchIDFlag, requireModuleFlag),
		Action: func(c *cli.Context) error {
			confPath := c.Parent().String(confFlagName)
			patchID := c.String(patchIDFlagName)
			module := c.String(moduleFlagName)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			conf, err := NewClientSettings(confPath)
			if err != nil {
				return errors.Wrap(err, "problem loading configuration")
			}

			client := conf.setupRestCommunicator(ctx)
			defer client.Close()

			ac, _, err := conf.getLegacyClients()
			if err != nil {
				return errors.Wrap(err, "problem accessing evergreen service")
			}

			err = ac.DeletePatchModule(patchID, module)
			if err != nil {
				return err
			}

			fmt.Println("Module removed.")
			return nil
		},
	}
}

// getModuleBranch returns the branch for the config.
func getModuleBranch(moduleName string, proj *model.Project) (string, error) {
	// find the module of the patch
	for _, module := range proj.Modules {
		if module.Name == moduleName {
			return module.Branch, nil
		}
	}
	return "", errors.Errorf("module '%s' unknown or not found", moduleName)
}
