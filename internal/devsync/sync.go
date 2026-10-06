// Package devsync keeps an archive in step with other computers through a
// shared folder (OneDrive, Dropbox, a network share...). Each computer
// publishes encrypted files describing its changes and merges everyone
// else's. See docs/adr/0007-multi-device-sync.md.
package devsync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"filippo.io/age"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// Keys in the archive's sync_meta table.
const (
	metaFolder   = "sync_folder"
	metaSet      = "sync_set"
	metaLastPull = "sync_last_pull"
	metaLastPush = "sync_last_push"
	cursorPrefix = "sync_cursor:"
)

// Compaction thresholds: a device folds its change files into a snapshot
// once it has this many, or its snapshot is this old.
var (
	CompactAfterFiles = 50
	CompactAfterAge   = 24 * time.Hour
)

// ErrNotConfigured means this archive does not sync.
var ErrNotConfigured = errors.New("sync is not set up for this archive")

// Syncer syncs one archive.
type Syncer struct {
	Store    *journal.Store
	Keychain Keychain
	// KeyCommand is sync.key_command, run when the keychain has no key.
	KeyCommand []string
	Now        func() time.Time

	keys []age.Identity // unlocked this process
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Config is where this archive syncs.
type Config struct {
	Folder string // the folder chosen by the person; files live in Folder/holocron-sync
	Set    string
}

// Configured returns the sync configuration, or ErrNotConfigured.
func (s *Syncer) Configured(ctx context.Context) (Config, error) {
	dir, err := s.Store.Meta(ctx, metaFolder)
	if err != nil {
		return Config{}, err
	}
	set, err := s.Store.Meta(ctx, metaSet)
	if err != nil {
		return Config{}, err
	}
	if dir == "" || set == "" {
		return Config{}, ErrNotConfigured
	}
	return Config{Folder: dir, Set: set}, nil
}

func folderAt(dir string) folder { return folder{root: filepath.Join(dir, DirName)} }

// Unlock says how to unlock the data key on this computer. Exactly one
// method is used, in the order of the fields.
type Unlock struct {
	Key           string
	Passphrase    string
	SSHKey        string // path to a private key
	SSHPassphrase func() ([]byte, error)
	KeyCommand    bool // run sync.key_command
}

func (s *Syncer) unlockWith(f folder, u Unlock) (*age.HybridIdentity, error) {
	switch {
	case u.Key != "":
		return parseDataKey(u.Key)
	case u.Passphrase != "":
		id, err := age.NewScryptIdentity(u.Passphrase)
		if err != nil {
			return nil, err
		}
		return unwrap(f.keysDir(), "passphrase", id)
	case u.SSHKey != "":
		id, err := sshIdentity(u.SSHKey, u.SSHPassphrase)
		if err != nil {
			return nil, err
		}
		return unwrap(f.keysDir(), "ssh-", id)
	case u.KeyCommand:
		out, err := runKeyCommand(s.KeyCommand)
		if err != nil {
			return nil, err
		}
		return parseDataKey(out)
	}
	return nil, errors.New("choose how to unlock: a key, a passphrase, an SSH key or sync.key_command")
}

// remember caches a data key (newest first) in the keychain and for this
// process. A keychain that cannot store it is reported but not fatal.
func (s *Syncer) remember(set string, key *age.HybridIdentity) error {
	s.keys = append([]age.Identity{key}, s.keys...)
	if s.Keychain == nil {
		return nil
	}
	existing, _ := s.Keychain.Get(set)
	keys := []string{key.String()}
	for _, k := range existing {
		if k != key.String() {
			keys = append(keys, k)
		}
	}
	if err := s.Keychain.Set(set, keys); err != nil {
		return fmt.Errorf("the key could not be saved in the OS keychain (%w); it will be needed again by each command", err)
	}
	return nil
}

// identities returns the data keys known on this computer, unlocking with
// the keychain or sync.key_command as needed.
func (s *Syncer) identities(set string) ([]age.Identity, error) { return s.identitiesVia(set, true) }

// identitiesVia is identities, optionally without running sync.key_command.
func (s *Syncer) identitiesVia(set string, keyCommand bool) ([]age.Identity, error) {
	if len(s.keys) > 0 {
		return s.keys, nil
	}
	if s.Keychain != nil {
		stored, err := s.Keychain.Get(set)
		if err == nil {
			for _, k := range stored {
				if id, err := parseDataKey(k); err == nil {
					s.keys = append(s.keys, id)
				}
			}
		}
		if len(s.keys) > 0 {
			return s.keys, nil
		}
	}
	if keyCommand && len(s.KeyCommand) > 0 {
		out, err := runKeyCommand(s.KeyCommand)
		if err != nil {
			return nil, err
		}
		id, err := parseDataKey(out)
		if err != nil {
			return nil, fmt.Errorf("sync.key_command: %w", err)
		}
		_ = s.remember(set, id)
		return s.keys, nil
	}
	return nil, fmt.Errorf("%w: unlock it with `holocron sync unlock`", ErrLocked)
}

// currentKey returns the data key files must now be encrypted with.
func (s *Syncer) currentKey(ff formatFile) (*age.HybridIdentity, error) {
	ids, err := s.identities(ff.Set)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		if h, ok := id.(*age.HybridIdentity); ok && keyID(h) == ff.KeyID {
			return h, nil
		}
	}
	return nil, fmt.Errorf("%w: the sync key was changed on another computer; unlock again with `holocron sync unlock`", ErrLocked)
}

// InitOptions configure a new sync folder.
type InitOptions struct {
	Name          string   // this computer's name; defaults to the host name
	Passphrase    string   // also allow unlocking with this passphrase
	SSHPublicKeys []string // also allow unlocking with these SSH keys
}

// InitResult reports a new sync folder.
type InitResult struct {
	Label string
	Key   string // the data key, to keep in a password manager
}

// Init turns dir into a sync folder holding this archive. This computer
// becomes device a; its existing entries keep their plain numbers.
func (s *Syncer) Init(ctx context.Context, dir string, opts InitOptions) (InitResult, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return InitResult{}, err
	}
	if _, err := s.Configured(ctx); err == nil {
		return InitResult{}, errors.New("this archive already syncs; see `holocron sync status`")
	}
	f := folderAt(dir)
	if _, err := os.Stat(f.formatPath()); err == nil {
		return InitResult{}, fmt.Errorf("%s is already a Holocron sync folder; join it with `holocron sync join`", dir)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return InitResult{}, fmt.Errorf("%s is not a folder", dir)
	}
	key, err := newDataKey()
	if err != nil {
		return InitResult{}, err
	}
	if err := s.writeUnlocks(f, key, opts.Passphrase, opts.SSHPublicKeys); err != nil {
		return InitResult{}, err
	}
	set := journal.NewUID(s.now())
	if err := f.writeFormat(formatFile{Format: FormatVersion, Set: set, KeyID: keyID(key), Created: s.now().UTC()}); err != nil {
		return InitResult{}, err
	}
	self, err := s.Store.SetSelfLabel(ctx, "a")
	if err != nil {
		return InitResult{}, err
	}
	if err := s.Store.SkipPlainNumbers(ctx); err != nil {
		return InitResult{}, err
	}
	res := InitResult{Label: self.Label, Key: key.String()}
	if err := s.configure(ctx, dir, set, key); err != nil {
		return res, err
	}
	if err := s.publishDevice(ctx, f, self, opts.Name, key); err != nil {
		return res, err
	}
	return res, s.compact(ctx, f, key, self.UID)
}

func (s *Syncer) writeUnlocks(f folder, key *age.HybridIdentity, passphrase string, sshKeys []string) error {
	if passphrase != "" {
		data, err := wrapWithPassphrase(key, passphrase)
		if err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(f.keysDir(), "passphrase.age"), data); err != nil {
			return err
		}
	}
	for _, pub := range sshKeys {
		name, data, err := wrapWithSSH(key, pub)
		if err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(f.keysDir(), name), data); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) configure(ctx context.Context, dir, set string, key *age.HybridIdentity) error {
	if err := s.Store.SetMeta(ctx, metaFolder, dir); err != nil {
		return err
	}
	if err := s.Store.SetMeta(ctx, metaSet, set); err != nil {
		return err
	}
	return s.remember(set, key)
}

func (s *Syncer) publishDevice(ctx context.Context, f folder, self journal.Device, name string, key *age.HybridIdentity) error {
	if name == "" {
		name, _ = os.Hostname()
	}
	if name != "" && name != self.Name {
		if err := s.Store.SetDeviceName(ctx, name); err != nil {
			return err
		}
	}
	return writeDevice(filepath.Join(f.deviceDir(self.UID), "device.age"),
		journal.DeviceRecord{UID: self.UID, Label: self.Label, Name: name}, key)
}

// JoinResult reports joining a sync folder.
type JoinResult struct {
	Label string
	// Renumbered lists this computer's own entries that took its label.
	Renumbered []journal.Renumbered
	Pull       PullResult
}

// Join connects this archive to an existing sync folder: it unlocks the
// key, takes the next free label, gives its own existing entries that label
// (keeping their digits), merges everything in the folder and publishes.
func (s *Syncer) Join(ctx context.Context, dir string, u Unlock, name string) (JoinResult, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return JoinResult{}, err
	}
	f := folderAt(dir)
	ff, err := f.readFormat()
	if err != nil {
		return JoinResult{}, err
	}
	if cfg, err := s.Configured(ctx); err == nil && cfg.Set != ff.Set {
		return JoinResult{}, errors.New("this archive already syncs with another folder; run `holocron sync off` first")
	}
	key, err := s.unlockWith(f, u)
	if err != nil {
		return JoinResult{}, err
	}
	if keyID(key) != ff.KeyID {
		return JoinResult{}, errors.New("that is not this folder's current sync key")
	}
	ids := []age.Identity{key}
	self, err := s.Store.SelfDevice(ctx)
	if err != nil {
		return JoinResult{}, err
	}
	uids, err := f.deviceUIDs()
	if err != nil {
		return JoinResult{}, err
	}
	used := map[string]bool{}
	label := ""
	for _, uid := range uids {
		d, err := readDevice(filepath.Join(f.deviceDir(uid), "device.age"), ids)
		if err != nil {
			continue
		}
		if uid == self.UID {
			label = d.Label // this archive was here before
			continue
		}
		used[d.Label] = true
	}
	if self.Label != "" {
		if used[self.Label] {
			return JoinResult{}, fmt.Errorf("this computer's label %q is used by another device in the folder", self.Label)
		}
		label = self.Label
	}
	if label == "" {
		if label, err = s.freeLabel(ctx, used); err != nil {
			return JoinResult{}, err
		}
	}
	res := JoinResult{Label: label}
	if self, err = s.Store.SetSelfLabel(ctx, label); err != nil {
		return res, err
	}
	// Entries the folder already has keep their numbers; this computer's own
	// entries take its label.
	known, err := s.folderUIDs(f, uids, self.UID, ids)
	if err != nil {
		return res, err
	}
	if res.Renumbered, err = s.Store.NumberOwnEntries(ctx, known); err != nil {
		return res, err
	}
	if err := s.configure(ctx, dir, ff.Set, key); err != nil {
		return res, err
	}
	if res.Pull, err = s.Pull(ctx); err != nil {
		return res, err
	}
	if err := s.Store.SkipPlainNumbers(ctx); err != nil {
		return res, err
	}
	if err := s.publishDevice(ctx, f, self, name, key); err != nil {
		return res, err
	}
	return res, s.compact(ctx, f, key, self.UID)
}

func (s *Syncer) freeLabel(ctx context.Context, used map[string]bool) (string, error) {
	devs, err := s.Store.Devices(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range devs {
		if !d.IsSelf && d.Label != "" {
			used[d.Label] = true
		}
	}
	for c := 'a'; c <= 'z'; c++ {
		if !used[string(c)] {
			return string(c), nil
		}
	}
	return "", errors.New("all 26 device labels are in use")
}

// folderUIDs collects the UIDs of every entry the other devices publish.
func (s *Syncer) folderUIDs(f folder, uids []string, self string, ids []age.Identity) (map[string]bool, error) {
	out := map[string]bool{}
	for _, uid := range uids {
		if uid == self {
			continue
		}
		files, err := f.listRecordFiles(uid)
		if err != nil {
			return nil, err
		}
		for _, rf := range fromLatestSnapshot(files) {
			recs, err := readRecords(filepath.Join(f.deviceDir(uid), rf.name), ids)
			if err != nil {
				return nil, err
			}
			for _, e := range recs.Entries {
				out[e.UID] = true
			}
		}
	}
	return out, nil
}

// fromLatestSnapshot returns the files needed to know everything: the
// latest snapshot and the changes after it.
func fromLatestSnapshot(files []recordFile) []recordFile {
	start := 0
	for i, rf := range files {
		if rf.snapshot {
			start = i
		}
	}
	return files[start:]
}

// Unlock unlocks and caches the data key on this computer.
func (s *Syncer) Unlock(ctx context.Context, u Unlock) error {
	cfg, err := s.Configured(ctx)
	if err != nil {
		return err
	}
	f := folderAt(cfg.Folder)
	ff, err := f.readFormat()
	if err != nil {
		return err
	}
	key, err := s.unlockWith(f, u)
	if err != nil {
		return err
	}
	if keyID(key) != ff.KeyID {
		return errors.New("that is not this folder's current sync key")
	}
	return s.remember(ff.Set, key)
}

// PullResult reports merging other devices' files.
type PullResult struct {
	Files    int
	Applied  journal.ApplyResult
	Warnings []string
}

// Pull merges every other device's new files. Problems with one device
// (a file not downloaded yet, a label clash) are warnings; the rest goes on.
func (s *Syncer) Pull(ctx context.Context) (PullResult, error) {
	var res PullResult
	cfg, err := s.Configured(ctx)
	if err != nil {
		return res, err
	}
	f := folderAt(cfg.Folder)
	ff, err := f.readFormat()
	if err != nil {
		return res, err
	}
	if ff.Set != cfg.Set {
		return res, fmt.Errorf("%s now holds a different sync set; run `holocron sync off` and join again", cfg.Folder)
	}
	self, err := s.Store.SelfDevice(ctx)
	if err != nil {
		return res, err
	}
	uids, err := f.deviceUIDs()
	if err != nil {
		return res, err
	}
	var ids []age.Identity
	for _, uid := range uids {
		if uid == self.UID {
			continue
		}
		files, err := f.listRecordFiles(uid)
		if err != nil {
			res.Warnings = append(res.Warnings, err.Error())
			continue
		}
		cursor, _ := s.cursor(ctx, uid)
		var fresh []recordFile
		for _, rf := range files {
			if rf.stamp > cursor {
				fresh = append(fresh, rf)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		if ids == nil {
			if ids, err = s.identities(cfg.Set); err != nil {
				return res, err
			}
		}
		d, err := readDevice(filepath.Join(f.deviceDir(uid), "device.age"), ids)
		if err != nil {
			if errors.Is(err, ErrLocked) {
				return res, err
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("device %s: %v", uid, err))
			continue
		}
		if d.Label == self.Label {
			if self.UID > uid {
				return res, fmt.Errorf("another computer (%s) also uses the label %q; run `holocron sync relabel` here", d.Name, self.Label)
			}
			res.Warnings = append(res.Warnings, fmt.Sprintf("%s also uses the label %q and must run `holocron sync relabel`; its changes are skipped until then", d.Name, d.Label))
			continue
		}
		for _, rf := range fromLatestSnapshot(fresh) {
			recs, err := readRecords(filepath.Join(f.deviceDir(uid), rf.name), ids)
			if err != nil {
				if errors.Is(err, ErrLocked) {
					return res, err
				}
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s: %v (will retry)", d.Name, err))
				break
			}
			applied, err := s.Store.Apply(ctx, recs, journal.ApplyOptions{})
			if err != nil {
				return res, fmt.Errorf("merging %s from %s: %w", rf.name, d.Name, err)
			}
			add(&res.Applied, applied)
			res.Files++
			if err := s.Store.SetMeta(ctx, cursorPrefix+uid, strconv.FormatInt(rf.stamp, 10)); err != nil {
				return res, err
			}
		}
	}
	return res, s.Store.SetMeta(ctx, metaLastPull, s.now().UTC().Format(time.RFC3339))
}

func add(total *journal.ApplyResult, r journal.ApplyResult) {
	total.EntriesAdded += r.EntriesAdded
	total.EntriesUpdated += r.EntriesUpdated
	total.EntriesUnchanged += r.EntriesUnchanged
	total.EntriesDeleted += r.EntriesDeleted
	total.EntriesSkipped += r.EntriesSkipped
	total.ProjectsAdded += r.ProjectsAdded
	total.ProjectsUpdated += r.ProjectsUpdated
	total.ProjectsMerged += r.ProjectsMerged
	total.ProjectsDeleted += r.ProjectsDeleted
	total.Reports += r.Reports
	total.Renumbered = append(total.Renumbered, r.Renumbered...)
	total.Warnings = append(total.Warnings, r.Warnings...)
}

func (s *Syncer) cursor(ctx context.Context, uid string) (int64, error) {
	v, err := s.Store.Meta(ctx, cursorPrefix+uid)
	if err != nil || v == "" {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// PushResult reports publishing this computer's changes.
type PushResult struct {
	Records   int
	Compacted bool
}

// Push publishes records changed here since the last push, and compacts
// this device's files when there are many or its snapshot is old.
func (s *Syncer) Push(ctx context.Context) (PushResult, error) {
	var res PushResult
	cfg, err := s.Configured(ctx)
	if err != nil {
		return res, err
	}
	f := folderAt(cfg.Folder)
	ff, err := f.readFormat()
	if err != nil {
		return res, err
	}
	recs, err := s.Store.ReadRecords(ctx, true)
	if err != nil {
		return res, err
	}
	res.Records = len(recs.Entries) + len(recs.Projects) + len(recs.Tombstones) + len(recs.Reports)
	if res.Records == 0 {
		return res, nil
	}
	key, err := s.currentKey(ff)
	if err != nil {
		return res, err
	}
	self, err := s.Store.SelfDevice(ctx)
	if err != nil {
		return res, err
	}
	files, err := f.listRecordFiles(self.UID)
	if err != nil {
		return res, err
	}
	if needsCompaction(files, s.now()) {
		res.Compacted = true
		return res, s.compact(ctx, f, key, self.UID)
	}
	stamp, err := f.nextStamp(self.UID, s.now())
	if err != nil {
		return res, err
	}
	if err := writeRecords(filepath.Join(f.deviceDir(self.UID), recordFileName(false, stamp)), recs, key); err != nil {
		return res, err
	}
	if err := s.Store.MarkPublished(ctx, recs); err != nil {
		return res, err
	}
	return res, s.Store.SetMeta(ctx, metaLastPush, s.now().UTC().Format(time.RFC3339))
}

func needsCompaction(files []recordFile, now time.Time) bool {
	changes := 0
	var snapshot time.Time
	for _, rf := range files {
		if rf.snapshot {
			snapshot = rf.time()
			changes = 0
		} else {
			changes++
		}
	}
	return snapshot.IsZero() || changes >= CompactAfterFiles || now.Sub(snapshot) >= CompactAfterAge
}

// Compact writes a fresh snapshot of this device and removes its older files.
func (s *Syncer) Compact(ctx context.Context) error {
	cfg, err := s.Configured(ctx)
	if err != nil {
		return err
	}
	f := folderAt(cfg.Folder)
	ff, err := f.readFormat()
	if err != nil {
		return err
	}
	key, err := s.currentKey(ff)
	if err != nil {
		return err
	}
	self, err := s.Store.SelfDevice(ctx)
	if err != nil {
		return err
	}
	return s.compact(ctx, f, key, self.UID)
}

func (s *Syncer) compact(ctx context.Context, f folder, key *age.HybridIdentity, selfUID string) error {
	recs, err := s.Store.ReadRecords(ctx, false)
	if err != nil {
		return err
	}
	stamp, err := f.nextStamp(selfUID, s.now())
	if err != nil {
		return err
	}
	if err := writeRecords(filepath.Join(f.deviceDir(selfUID), recordFileName(true, stamp)), recs, key); err != nil {
		return err
	}
	if err := s.Store.MarkPublished(ctx, recs); err != nil {
		return err
	}
	// Only once the snapshot is written do the files it replaces go.
	files, err := f.listRecordFiles(selfUID)
	if err != nil {
		return err
	}
	for _, rf := range files {
		if rf.stamp < stamp {
			if err := os.Remove(filepath.Join(f.deviceDir(selfUID), rf.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return s.Store.SetMeta(ctx, metaLastPush, s.now().UTC().Format(time.RFC3339))
}

// DeviceStatus describes one device in the folder.
type DeviceStatus struct {
	UID, Label, Name string
	Self             bool
	LastPublished    time.Time
}

// Status describes sync on this computer.
type Status struct {
	Folder     string
	Label      string
	Locked     bool
	KeyCommand bool // sync.key_command is configured
	Devices    []DeviceStatus
	Pending    int
	LastPull   time.Time
	LastPush   time.Time
	Problem    string // why the folder cannot be read, if it cannot
}

// Status reports the state of sync without changing anything.
func (s *Syncer) Status(ctx context.Context) (Status, error) {
	var st Status
	cfg, err := s.Configured(ctx)
	if err != nil {
		return st, err
	}
	st.Folder = cfg.Folder
	self, err := s.Store.SelfDevice(ctx)
	if err != nil {
		return st, err
	}
	st.Label = self.Label
	if st.Pending, err = s.Store.Pending(ctx); err != nil {
		return st, err
	}
	for key, dst := range map[string]*time.Time{metaLastPull: &st.LastPull, metaLastPush: &st.LastPush} {
		if v, _ := s.Store.Meta(ctx, key); v != "" {
			*dst, _ = time.Parse(time.RFC3339, v)
		}
	}
	// A folder that cannot be read is part of the status, not a failure.
	f := folderAt(cfg.Folder)
	var uids []string
	if _, ferr := f.readFormat(); ferr != nil {
		st.Problem = ferr.Error()
	} else if uids, ferr = f.deviceUIDs(); ferr != nil {
		st.Problem = ferr.Error()
	}
	if st.Problem != "" {
		return st, nil
	}
	// Status never runs sync.key_command, which might prompt.
	ids, err := s.identitiesVia(cfg.Set, false)
	st.Locked = err != nil && len(s.KeyCommand) == 0
	st.KeyCommand = len(s.KeyCommand) > 0
	for _, uid := range uids {
		ds := DeviceStatus{UID: uid, Self: uid == self.UID}
		if !st.Locked {
			if d, err := readDevice(filepath.Join(f.deviceDir(uid), "device.age"), ids); err == nil {
				ds.Label, ds.Name = d.Label, d.Name
			}
		}
		if files, _ := f.listRecordFiles(uid); len(files) > 0 {
			ds.LastPublished = files[len(files)-1].time()
		}
		st.Devices = append(st.Devices, ds)
	}
	slices.SortFunc(st.Devices, func(a, b DeviceStatus) int {
		switch {
		case a.Self != b.Self:
			if a.Self {
				return -1
			}
			return 1
		case a.Label < b.Label:
			return -1
		case a.Label > b.Label:
			return 1
		}
		return 0
	})
	return st, nil
}

// Relabel gives this computer the next free label, when another device
// turned out to have the same one.
func (s *Syncer) Relabel(ctx context.Context) (string, error) {
	cfg, err := s.Configured(ctx)
	if err != nil {
		return "", err
	}
	f := folderAt(cfg.Folder)
	ff, err := f.readFormat()
	if err != nil {
		return "", err
	}
	key, err := s.currentKey(ff)
	if err != nil {
		return "", err
	}
	self, err := s.Store.SelfDevice(ctx)
	if err != nil {
		return "", err
	}
	uids, err := f.deviceUIDs()
	if err != nil {
		return "", err
	}
	used := map[string]bool{self.Label: true}
	for _, uid := range uids {
		if d, err := readDevice(filepath.Join(f.deviceDir(uid), "device.age"), []age.Identity{key}); err == nil && uid != self.UID {
			used[d.Label] = true
		}
	}
	label, err := s.freeLabel(ctx, used)
	if err != nil {
		return "", err
	}
	if self, err = s.Store.Relabel(ctx, label); err != nil {
		return "", err
	}
	if err := s.publishDevice(ctx, f, self, self.Name, key); err != nil {
		return "", err
	}
	return label, s.compact(ctx, f, key, self.UID)
}

// AddPassphrase lets the folder also be unlocked with a passphrase,
// replacing any passphrase set before.
func (s *Syncer) AddPassphrase(ctx context.Context, passphrase string) error {
	return s.withKey(ctx, func(f folder, key *age.HybridIdentity) error {
		return s.writeUnlocks(f, key, passphrase, nil)
	})
}

// AddSSHKey lets the folder also be unlocked with an SSH key.
func (s *Syncer) AddSSHKey(ctx context.Context, publicKey string) error {
	return s.withKey(ctx, func(f folder, key *age.HybridIdentity) error {
		return s.writeUnlocks(f, key, "", []string{publicKey})
	})
}

// Key returns the data key, for keeping in a password manager or pasting
// into `holocron sync join` on another computer.
func (s *Syncer) Key(ctx context.Context) (string, error) {
	var out string
	err := s.withKey(ctx, func(_ folder, key *age.HybridIdentity) error {
		out = key.String()
		return nil
	})
	return out, err
}

func (s *Syncer) withKey(ctx context.Context, fn func(folder, *age.HybridIdentity) error) error {
	cfg, err := s.Configured(ctx)
	if err != nil {
		return err
	}
	f := folderAt(cfg.Folder)
	ff, err := f.readFormat()
	if err != nil {
		return err
	}
	key, err := s.currentKey(ff)
	if err != nil {
		return err
	}
	return fn(f, key)
}

// RotateKey replaces the data key. Every unlock method is removed and set up
// again from opts, and this computer rewrites its files with the new key.
// Other computers must unlock again; until they do, their files stay
// readable with the old key, which is kept.
func (s *Syncer) RotateKey(ctx context.Context, opts InitOptions) (string, error) {
	cfg, err := s.Configured(ctx)
	if err != nil {
		return "", err
	}
	f := folderAt(cfg.Folder)
	ff, err := f.readFormat()
	if err != nil {
		return "", err
	}
	if _, err := s.currentKey(ff); err != nil {
		return "", err
	}
	key, err := newDataKey()
	if err != nil {
		return "", err
	}
	old, _ := filepath.Glob(filepath.Join(f.keysDir(), "*.age"))
	for _, p := range old {
		if err := os.Remove(p); err != nil {
			return "", err
		}
	}
	if err := s.writeUnlocks(f, key, opts.Passphrase, opts.SSHPublicKeys); err != nil {
		return "", err
	}
	ff.KeyID = keyID(key)
	if err := f.writeFormat(ff); err != nil {
		return "", err
	}
	_ = s.remember(cfg.Set, key)
	self, err := s.Store.SelfDevice(ctx)
	if err != nil {
		return "", err
	}
	if err := s.publishDevice(ctx, f, self, self.Name, key); err != nil {
		return "", err
	}
	return key.String(), s.compact(ctx, f, key, self.UID)
}

// Off stops syncing this archive. The folder and other computers are left
// alone, and entries keep their numbers.
func (s *Syncer) Off(ctx context.Context) error {
	cfg, err := s.Configured(ctx)
	if err != nil {
		return err
	}
	for _, k := range []string{metaFolder, metaSet, metaLastPull, metaLastPush} {
		if err := s.Store.SetMeta(ctx, k, ""); err != nil {
			return err
		}
	}
	if s.Keychain != nil {
		return s.Keychain.Delete(cfg.Set)
	}
	return nil
}
