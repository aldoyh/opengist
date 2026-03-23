package cli

import (
	"fmt"
	"github.com/thomiceli/opengist/internal/actions"
	"github.com/thomiceli/opengist/internal/auth/password"
	"github.com/thomiceli/opengist/internal/db"
	"github.com/urfave/cli/v2"
)

var CmdAdmin = cli.Command{
	Name:  "admin",
	Usage: "Admin commands",
	Subcommands: []*cli.Command{
		&CmdAdminResetPassword,
		&CmdAdminToggleAdmin,
		&CmdAdminSyncGithubGists,
	},
}

var CmdAdminResetPassword = cli.Command{
	Name:      "reset-password",
	Usage:     "Reset the password for a given user",
	ArgsUsage: "[username] [password]",
	Action: func(ctx *cli.Context) error {
		initialize(ctx)
		if ctx.NArg() < 2 {
			return fmt.Errorf("username and password are required")
		}
		username := ctx.Args().Get(0)
		plainPassword := ctx.Args().Get(1)

		user, err := db.GetUserByUsername(username)
		if err != nil {
			fmt.Printf("Cannot get user %s: %s\n", username, err)
			return err
		}
		password, err := password.HashPassword(plainPassword)
		if err != nil {
			fmt.Printf("Cannot hash password for user %s: %s\n", username, err)
			return err
		}
		user.Password = password

		if err = user.Update(); err != nil {
			fmt.Printf("Cannot update password for user %s: %s\n", username, err)
			return err
		}

		fmt.Printf("Password for user %s has been reset.\n", username)
		return nil
	},
}

var CmdAdminToggleAdmin = cli.Command{
	Name:      "toggle-admin",
	Usage:     "Toggle the admin status for a given user",
	ArgsUsage: "[username]",
	Action: func(ctx *cli.Context) error {
		initialize(ctx)
		if ctx.NArg() < 1 {
			return fmt.Errorf("username is required")
		}
		username := ctx.Args().Get(0)

		user, err := db.GetUserByUsername(username)
		if err != nil {
			fmt.Printf("Cannot get user %s: %s\n", username, err)
			return err
		}

		user.IsAdmin = !user.IsAdmin
		if err = user.Update(); err != nil {
			fmt.Printf("Cannot update user %s: %s\n", username, err)
		}

		fmt.Printf("User %s admin set to %t\n", username, user.IsAdmin)
		return nil
	},
}

var CmdAdminSyncGithubGists = cli.Command{
	Name:      "sync-github-gists",
	Usage:     "Synchronize GitHub Gists into Opengist for users with linked GitHub accounts",
	ArgsUsage: "[username]",
	Action: func(ctx *cli.Context) error {
		initialize(ctx)

		if ctx.NArg() >= 1 {
			// Sync a specific user
			username := ctx.Args().Get(0)
			fmt.Printf("Syncing GitHub gists for user %s...\n", username)
			if err := actions.SyncGithubGistsForUser(username); err != nil {
				fmt.Printf("Error syncing GitHub gists for user %s: %s\n", username, err)
				return err
			}
			fmt.Printf("GitHub gists for user %s have been synced.\n", username)
		} else {
			// Sync all users with linked GitHub accounts
			fmt.Println("Syncing GitHub gists for all users with linked GitHub accounts...")
			actions.Run(actions.SyncGithubGists)
			fmt.Println("GitHub gist sync complete.")
		}

		return nil
	},
}
