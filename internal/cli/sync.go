package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Unscheduled-Maintenance/Holocron/internal/app"
	"github.com/Unscheduled-Maintenance/Holocron/internal/devsync"
)

func newSyncCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Keep this archive in step with your other computers",
		Long: `Sync keeps one archive in step across computers through a shared folder,
such as a OneDrive, Dropbox or iCloud Drive folder. Each computer keeps its own
archive and works offline; changes are encrypted and published to the folder,
and each computer merges the others' changes. For every field the most recent
change wins, and deletes win over older changes.

Once set up, every command syncs by itself: it merges what other computers
published before it starts and publishes this computer's changes when it
finishes. Running "holocron sync" does both now and reports the result.

A computer can be receive-only: it merges the other computers' changes but
never publishes its own, so notes made there stay there. Use --receive-only
with init or join, or "holocron sync mode" later.

Each computer gets a letter: new entries are numbered #12a on the first
computer, #12b on the second, so a number always means the same entry
everywhere. Entries made before sync was set up keep their plain numbers.

Get started:
  holocron sync init ~/OneDrive/Holocron        on the first computer
  holocron sync join ~/OneDrive/Holocron        on each of the others

Check with your employer before syncing work notes through a personal folder.
See docs/adr/0007-multi-device-sync.md for the design.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.openNoSync(ctx)
			if err != nil {
				return err
			}
			pull, err := a.Sync.Pull(ctx)
			if errors.Is(err, devsync.ErrNotConfigured) {
				return usagef("sync is not set up; see `holocron sync --help`")
			}
			if err != nil {
				return err
			}
			for _, w := range append(pull.Warnings, pull.Applied.Warnings...) {
				e.note("%s %s", e.errStyle().Warn("warning:"), w)
			}
			push, err := a.Sync.Push(ctx)
			if err != nil {
				return err
			}
			st := e.out()
			fmt.Fprintf(e.io.Out, "%s %s\n", st.Success("Synced."), describePull(pull))
			if push.Records > 0 {
				fmt.Fprintf(e.io.Out, "Published %s from this computer.\n", count(push.Records, "change", "changes"))
			}
			return nil
		},
	}
	cmd.AddCommand(newSyncInitCmd(e), newSyncJoinCmd(e), newSyncUnlockCmd(e), newSyncStatusCmd(e),
		newSyncCompactCmd(e), newSyncRelabelCmd(e), newSyncModeCmd(e), newSyncKeyCmd(e), newSyncOffCmd(e))
	return cmd
}

func describePull(r devsync.PullResult) string {
	a := r.Applied
	if a.EntriesAdded+a.EntriesUpdated+a.EntriesDeleted+a.ProjectsAdded+a.ProjectsUpdated+a.ProjectsMerged+a.ProjectsDeleted == 0 {
		return "Nothing new from other computers."
	}
	var parts []string
	if a.EntriesAdded > 0 {
		parts = append(parts, count(a.EntriesAdded, "new entry", "new entries"))
	}
	if a.EntriesUpdated > 0 {
		parts = append(parts, count(a.EntriesUpdated, "updated entry", "updated entries"))
	}
	if a.EntriesDeleted > 0 {
		parts = append(parts, count(a.EntriesDeleted, "deleted entry", "deleted entries"))
	}
	if n := a.ProjectsAdded + a.ProjectsUpdated + a.ProjectsMerged + a.ProjectsDeleted; n > 0 {
		parts = append(parts, count(n, "project change", "project changes"))
	}
	return "From other computers: " + strings.Join(parts, ", ") + "."
}

// unlockFlags choose how to unlock the sync key.
type unlockFlags struct {
	key        bool
	passphrase bool
	sshKey     string
	keyCommand bool
}

func (f *unlockFlags) register(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.BoolVar(&f.key, "key", false, "enter the sync key (read from stdin when not at a terminal)")
	fs.BoolVar(&f.passphrase, "passphrase", false, "enter the folder's passphrase")
	fs.StringVar(&f.sshKey, "ssh-key", "", "unlock with this SSH private key (for example ~/.ssh/id_ed25519)")
	fs.BoolVar(&f.keyCommand, "key-command", false, "run sync.key_command from the config to get the key")
	cmd.MarkFlagsMutuallyExclusive("key", "passphrase", "ssh-key", "key-command")
}

func (f *unlockFlags) unlock(e *env, a *app.App) (devsync.Unlock, error) {
	switch {
	case f.passphrase:
		p, err := readSecret(e, "Passphrase: ")
		return devsync.Unlock{Passphrase: p}, err
	case f.sshKey != "":
		return devsync.Unlock{SSHKey: expandPath(f.sshKey), SSHPassphrase: func() ([]byte, error) {
			p, err := readSecret(e, "Passphrase for "+f.sshKey+": ")
			return []byte(p), err
		}}, nil
	case f.keyCommand:
		return devsync.Unlock{KeyCommand: true}, nil
	case !f.key && len(a.Sync.KeyCommand) > 0:
		return devsync.Unlock{KeyCommand: true}, nil
	}
	k, err := readSecret(e, "Sync key: ")
	return devsync.Unlock{Key: k}, err
}

func (f *unlockFlags) registerForBackup(cmd *cobra.Command) {
	fs := cmd.Flags()
	fs.BoolVar(&f.key, "key", false, "enter the sync key an encrypted backup was made with")
	fs.BoolVar(&f.passphrase, "passphrase", false, "enter an encrypted backup's passphrase")
	fs.StringVar(&f.sshKey, "ssh-key", "", "open an encrypted backup with this SSH private key")
	fs.BoolVar(&f.keyCommand, "key-command", false, "run sync.key_command from the config to get the sync key")
	cmd.MarkFlagsMutuallyExclusive("key", "passphrase", "ssh-key", "key-command")
}

// backup chooses how to open an encrypted backup. Without a flag, a
// passphrase is asked for if the backup has one, and otherwise the sync keys
// this computer keeps (then sync.key_command) are tried.
func (f *unlockFlags) backup(e *env, kind devsync.BackupKind, path string) (devsync.Unlock, error) {
	switch {
	case f.passphrase || (kind.Passphrase && !f.key && f.sshKey == "" && !f.keyCommand):
		p, err := readSecret(e, "Passphrase: ")
		return devsync.Unlock{Passphrase: p}, err
	case f.sshKey != "":
		return devsync.Unlock{SSHKey: expandPath(f.sshKey), SSHPassphrase: func() ([]byte, error) {
			p, err := readSecret(e, "Passphrase for "+f.sshKey+": ")
			return []byte(p), err
		}}, nil
	case f.keyCommand:
		return devsync.Unlock{KeyCommand: true}, nil
	case f.key:
		k, err := readSecret(e, "Sync key: ")
		return devsync.Unlock{Key: k}, err
	case kind.SSH && !kind.Key:
		return devsync.Unlock{}, usagef("%s is encrypted to an SSH key; open it with --ssh-key and the private key (for example ~/.ssh/id_ed25519)", path)
	}
	return devsync.Unlock{}, nil
}

// readSecret reads a line without echoing it at a terminal.
func readSecret(e *env, prompt string) (string, error) {
	if f, ok := e.io.In.(*os.File); ok && e.io.InTTY && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(e.io.Err, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(e.io.Err)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(e.io.In).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("expected %s on stdin", strings.TrimSuffix(strings.ToLower(prompt), ": "))
	}
	return strings.TrimSpace(line), nil
}

func newSyncInitCmd(e *env) *cobra.Command {
	var passphrase, receiveOnly bool
	var sshKeys []string
	var name string
	cmd := &cobra.Command{
		Use:   "init <folder>",
		Short: "Start syncing this archive through a folder",
		Long: `Set up a folder (for example in OneDrive) to sync this archive, with this
computer as device "a". Existing entries keep their numbers; new ones are
numbered #12a.

A sync key is generated and printed once. Keep it in your password manager:
other computers need it, or one of these, to join:
  --passphrase       also allow joining with a passphrase you choose
  --ssh-key PUB      also allow joining with an SSH key (its .pub file; repeatable)

To fetch the key from a password manager instead of typing it, set
sync.key_command in the config, for example:
  [sync]
  key_command = "op read op://Private/Holocron/sync-key"`,
		Example: "  holocron sync init ~/OneDrive/Holocron\n  holocron sync init ~/OneDrive/Holocron --passphrase",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.openNoSync(ctx)
			if err != nil {
				return err
			}
			opts := devsync.InitOptions{Name: name, ReceiveOnly: receiveOnly}
			if passphrase {
				if opts.Passphrase, err = choosePassphrase(e); err != nil {
					return err
				}
			}
			for _, path := range sshKeys {
				pub, err := os.ReadFile(expandPath(path))
				if err != nil {
					return err
				}
				opts.SSHPublicKeys = append(opts.SSHPublicKeys, string(pub))
			}
			res, err := a.Sync.Init(ctx, expandPath(args[0]), opts)
			if err != nil {
				return err
			}
			st := e.out()
			fmt.Fprintf(e.io.Out, "%s This computer is device %q; new entries will be numbered like #1%s.\n\n",
				st.Success("Sync is set up."), res.Label, res.Label)
			if receiveOnly {
				fmt.Fprintf(e.io.Out, "%s\n\n", receiveOnlyNote)
			}
			fmt.Fprintf(e.io.Out, "Sync key (shown once; keep it in your password manager):\n\n  %s\n\n", res.Key)
			fmt.Fprintf(e.io.Out, "On each other computer run:  holocron sync join %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&passphrase, "passphrase", false, "also allow unlocking with a passphrase")
	cmd.Flags().StringSliceVar(&sshKeys, "ssh-key", nil, "also allow unlocking with this SSH public key file (repeatable)")
	cmd.Flags().StringVar(&name, "name", "", "a name for this computer (default: its host name)")
	cmd.Flags().BoolVar(&receiveOnly, "receive-only", false, receiveOnlyFlag)
	return cmd
}

func newSyncJoinCmd(e *env) *cobra.Command {
	f := &unlockFlags{}
	var name string
	var receiveOnly bool
	cmd := &cobra.Command{
		Use:   "join <folder>",
		Short: "Join a sync folder set up on another computer",
		Long: `Connect this archive to a sync folder made with "holocron sync init". This
computer takes the next free letter (b, c, ...). Entries already in this
archive keep their digits and take its letter (#3 becomes #3b), then
everything in the folder is merged in.

The key is asked for unless sync.key_command is configured; or unlock with
--passphrase or --ssh-key if those were set up.

With --receive-only, this computer merges the folder's changes but never
publishes its own entries.`,
		Example: "  holocron sync join ~/OneDrive/Holocron\n  holocron sync join ~/OneDrive/Holocron --passphrase",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.openNoSync(ctx)
			if err != nil {
				return err
			}
			u, err := f.unlock(e, a)
			if err != nil {
				return err
			}
			res, err := a.Sync.Join(ctx, expandPath(args[0]), u, devsync.JoinOptions{Name: name, ReceiveOnly: receiveOnly})
			if err != nil {
				return err
			}
			st := e.out()
			fmt.Fprintf(e.io.Out, "%s This computer is device %q. %s\n", st.Success("Joined."), res.Label, describePull(res.Pull))
			if len(res.Renumbered) > 0 {
				fmt.Fprintf(e.io.Out, "%s here took this computer's letter, keeping their digits (%s → %s, ...).\n",
					count(len(res.Renumbered), "entry", "entries"), res.Renumbered[0].From, res.Renumbered[0].To)
			}
			if receiveOnly {
				fmt.Fprintln(e.io.Out, receiveOnlyNote)
			}
			for _, w := range res.Pull.Warnings {
				e.note("%s %s", e.errStyle().Warn("warning:"), w)
			}
			return nil
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&name, "name", "", "a name for this computer (default: its host name)")
	cmd.Flags().BoolVar(&receiveOnly, "receive-only", false, receiveOnlyFlag)
	return cmd
}

func newSyncUnlockCmd(e *env) *cobra.Command {
	f := &unlockFlags{}
	cmd := &cobra.Command{
		Use:   "unlock",
		Short: "Unlock sync on this computer (for example after the key changed)",
		Long: `Unlock the sync key and keep it in this computer's keychain (Windows
Credential Manager, the macOS Keychain or the Linux Secret Service). Needed
when the keychain lost the key, or after "holocron sync key rotate" on
another computer.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.openNoSync(ctx)
			if err != nil {
				return err
			}
			u, err := f.unlock(e, a)
			if err != nil {
				return err
			}
			if err := a.Sync.Unlock(ctx, u); err != nil {
				return err
			}
			fmt.Fprintln(e.io.Out, e.out().Success("Unlocked."))
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

func newSyncStatusCmd(e *env) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the sync folder, the computers in it and what is waiting",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.openNoSync(ctx)
			if err != nil {
				return err
			}
			s, err := a.Sync.Status(ctx)
			if errors.Is(err, devsync.ErrNotConfigured) {
				if asJSON {
					return writeJSON(e.io.Out, map[string]any{"enabled": false})
				}
				fmt.Fprintln(e.io.Out, "Sync is not set up. See `holocron sync --help`.")
				return nil
			}
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(e.io.Out, syncStatusJSON(s))
			}
			printSyncStatus(e, a, s)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func printSyncStatus(e *env, a *app.App, s devsync.Status) {
	st := e.out()
	w := e.io.Out
	when := func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		return t.In(a.Loc).Format("Mon 2 Jan 2006 " + a.TimeLayout())
	}
	fmt.Fprintf(w, "%s  %s\n", st.Dim("folder  "), s.Folder)
	fmt.Fprintf(w, "%s  %s\n", st.Dim("this    "), "device "+s.Label)
	if s.ReceiveOnly {
		fmt.Fprintf(w, "%s  %s\n", st.Dim("mode    "), "receive-only: merges other computers' changes, publishes none")
	}
	if s.Problem != "" {
		fmt.Fprintf(w, "%s  %s\n", st.Dim("problem "), st.Warn(s.Problem))
	}
	if s.Locked {
		fmt.Fprintf(w, "%s  %s\n", st.Dim("key     "), st.Warn("locked: run `holocron sync unlock`"))
	}
	if !s.ReceiveOnly {
		pending := "nothing"
		if s.Pending > 0 {
			pending = count(s.Pending, "change", "changes") + " not yet published (published by the next command, or `holocron sync`)"
		}
		fmt.Fprintf(w, "%s  %s\n", st.Dim("waiting "), pending)
	}
	fmt.Fprintf(w, "%s  merged %s · published %s\n", st.Dim("last    "), when(s.LastPull), when(s.LastPush))
	if len(s.Devices) > 0 {
		fmt.Fprintln(w)
		for _, d := range s.Devices {
			name := d.Name
			if name == "" {
				name = d.UID
			}
			label := d.Label
			if label == "" {
				label = "?"
			}
			you := ""
			if d.Self {
				you = st.Dim(" (this computer)")
			}
			fmt.Fprintf(w, "  %s  %s%s  %s\n", st.Accent(label), name, you, st.Dim("last published "+when(d.LastPublished)))
		}
	}
}

func syncStatusJSON(s devsync.Status) map[string]any {
	ts := func(t time.Time) any {
		if t.IsZero() {
			return nil
		}
		return t.UTC()
	}
	devices := []map[string]any{}
	for _, d := range s.Devices {
		devices = append(devices, map[string]any{"uid": d.UID, "label": d.Label, "name": d.Name, "self": d.Self,
			"last_published": ts(d.LastPublished)})
	}
	return map[string]any{"enabled": true, "folder": s.Folder, "label": s.Label, "receive_only": s.ReceiveOnly, "locked": s.Locked,
		"pending": s.Pending, "last_merged": ts(s.LastPull), "last_published": ts(s.LastPush),
		"problem": s.Problem, "devices": devices}
}

func newSyncCompactCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "compact",
		Short: "Replace this computer's sync files with one snapshot",
		Long: `Write a fresh snapshot of this archive to the sync folder and remove this
computer's older sync files. This happens by itself once a day or after 50
changes; run it to tidy up sooner.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.openNoSync(cmd.Context())
			if err != nil {
				return err
			}
			if err := a.Sync.Compact(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(e.io.Out, e.out().Success("Compacted."))
			return nil
		},
	}
}

func newSyncRelabelCmd(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "relabel",
		Short: "Give this computer a new letter when another one has the same",
		Long: `Two computers can end up with the same letter, for example when both joined
before the folder finished syncing. Sync then asks the one that joined later
to run this: it takes the next free letter, and the entries it numbered are
shown with that letter everywhere (#12b becomes #12c).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.openNoSync(cmd.Context())
			if err != nil {
				return err
			}
			label, err := a.Sync.Relabel(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(e.io.Out, "%s This computer is now device %q.\n", e.out().Success("Relabelled."), label)
			return nil
		},
	}
}

const (
	receiveOnlyFlag = "merge other computers' changes but never publish this one's"
	receiveOnlyNote = "This computer is receive-only: it merges other computers' changes and publishes none of its own."
)

func newSyncModeCmd(e *env) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "mode [two-way|receive-only]",
		Short: "Show or change whether this computer publishes its changes",
		Long: `Show whether this computer syncs two ways or is receive-only, or change it.

A receive-only computer merges the other computers' changes but never
publishes its own, for example a work laptop whose notes must stay on it
while notes from a personal laptop still arrive. Its edits to entries from
other computers stay on it too, and a later edit to the same field elsewhere
replaces them.

Switching to receive-only removes this computer's files from the sync folder.
Computers that already merged them keep what they have.

Switching to two-way publishes everything in this archive, so it asks first.`,
		Example:   "  holocron sync mode\n  holocron sync mode receive-only\n  holocron sync mode two-way",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"two-way", "receive-only"},
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			a, err := e.openNoSync(ctx)
			if err != nil {
				return err
			}
			cfg, err := a.Sync.Configured(ctx)
			if errors.Is(err, devsync.ErrNotConfigured) {
				return usagef("sync is not set up; see `holocron sync --help`")
			}
			if err != nil {
				return err
			}
			current := map[bool]string{true: "receive-only", false: "two-way"}[cfg.ReceiveOnly]
			if len(args) == 0 {
				fmt.Fprintln(e.io.Out, current)
				return nil
			}
			var receiveOnly bool
			switch args[0] {
			case "receive-only":
				receiveOnly = true
			case "two-way":
			default:
				return usagef("unknown mode %q; use two-way or receive-only", args[0])
			}
			if args[0] == current {
				fmt.Fprintf(e.io.Out, "This computer is already %s.\n", current)
				return nil
			}
			if !receiveOnly && !yes && !confirm(e, "Publish every entry and project on this computer to the sync folder?") {
				if !e.io.InTTY {
					return usagef("pass --yes to publish this computer's entries")
				}
				e.note("Nothing changed.")
				return nil
			}
			if err := a.Sync.SetReceiveOnly(ctx, receiveOnly); err != nil {
				return err
			}
			if receiveOnly {
				fmt.Fprintf(e.io.Out, "%s %s\n", e.out().Success("Done."), receiveOnlyNote)
			} else {
				fmt.Fprintf(e.io.Out, "%s This computer syncs two ways and has published its entries.\n", e.out().Success("Done."))
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before publishing (two-way)")
	return cmd
}

func newSyncOffCmd(e *env) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "off",
		Short: "Stop syncing this archive",
		Long: `Stop syncing this archive and forget the key on this computer. Nothing is
deleted: the archive keeps every entry and number, and the folder and other
computers are left as they are.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes && !confirm(e, "Stop syncing this archive?") {
				if !e.io.InTTY {
					return usagef("pass --yes to stop syncing")
				}
				e.note("Nothing changed.")
				return nil
			}
			a, err := e.openNoSync(cmd.Context())
			if err != nil {
				return err
			}
			if err := a.Sync.Off(cmd.Context()); err != nil {
				return err
			}
			fmt.Fprintln(e.io.Out, e.out().Success("Sync is off for this archive."))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newSyncKeyCmd(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "key",
		Short: "Show the sync key, add ways to unlock it, or replace it",
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the sync key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.openNoSync(cmd.Context())
			if err != nil {
				return err
			}
			k, err := a.Sync.Key(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintln(e.io.Out, k)
			return nil
		},
	}
	passphrase := &cobra.Command{
		Use:   "passphrase",
		Short: "Allow unlocking with a passphrase (replacing any earlier one)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.openNoSync(cmd.Context())
			if err != nil {
				return err
			}
			p, err := choosePassphrase(e)
			if err != nil {
				return err
			}
			if err := a.Sync.AddPassphrase(cmd.Context(), p); err != nil {
				return err
			}
			fmt.Fprintln(e.io.Out, e.out().Success("Other computers can now unlock with that passphrase."))
			return nil
		},
	}
	sshCmd := &cobra.Command{
		Use:   "ssh <public-key-file>",
		Short: "Allow unlocking with an SSH key (ed25519 or RSA; the key file itself must be on disk)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.openNoSync(cmd.Context())
			if err != nil {
				return err
			}
			pub, err := os.ReadFile(expandPath(args[0]))
			if err != nil {
				return err
			}
			if err := a.Sync.AddSSHKey(cmd.Context(), string(pub)); err != nil {
				return err
			}
			fmt.Fprintln(e.io.Out, e.out().Success("Other computers can now unlock with that SSH key."))
			return nil
		},
	}
	var rotPass bool
	var rotSSH []string
	rotate := &cobra.Command{
		Use:   "rotate",
		Short: "Replace the sync key",
		Long: `Replace the sync key, for example if it may have leaked. Every way of
unlocking is removed; add them again with --passphrase and --ssh-key. This
computer rewrites its sync files with the new key, and every other computer
must run "holocron sync unlock" with the new key once.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := e.openNoSync(cmd.Context())
			if err != nil {
				return err
			}
			var opts devsync.InitOptions
			if rotPass {
				if opts.Passphrase, err = choosePassphrase(e); err != nil {
					return err
				}
			}
			for _, path := range rotSSH {
				pub, err := os.ReadFile(expandPath(path))
				if err != nil {
					return err
				}
				opts.SSHPublicKeys = append(opts.SSHPublicKeys, string(pub))
			}
			k, err := a.Sync.RotateKey(cmd.Context(), opts)
			if err != nil {
				return err
			}
			fmt.Fprintf(e.io.Out, "%s New sync key (keep it in your password manager):\n\n  %s\n\n", e.out().Success("Key replaced."), k)
			fmt.Fprintln(e.io.Out, "Run `holocron sync unlock` on each other computer.")
			return nil
		},
	}
	rotate.Flags().BoolVar(&rotPass, "passphrase", false, "allow unlocking with a new passphrase")
	rotate.Flags().StringSliceVar(&rotSSH, "ssh-key", nil, "allow unlocking with this SSH public key file (repeatable)")
	cmd.AddCommand(show, passphrase, sshCmd, rotate)
	return cmd
}

func choosePassphrase(e *env) (string, error) {
	p, err := readSecret(e, "Choose a passphrase: ")
	if err != nil || !e.io.InTTY {
		// Piped input is read once: there is no typing to mistake.
		return p, err
	}
	again, err := readSecret(e, "Repeat it: ")
	if err != nil {
		return "", err
	}
	if p != again {
		return "", usagef("the passphrases do not match")
	}
	return p, nil
}
