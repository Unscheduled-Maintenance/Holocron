package devsync

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/Unscheduled-Maintenance/Holocron/internal/journal"
)

// FormatVersion identifies the sync folder layout.
const FormatVersion = "holocron.sync/v1"

// DirName is the directory Holocron keeps inside the chosen sync folder.
const DirName = "holocron-sync"

// The folder layout (docs/adr/0007-multi-device-sync.md):
//
//	holocron-sync/
//	  format.json                 version, set ID and key ID (not secret)
//	  keys/                       the data key, wrapped per unlock method
//	  devices/<device-uid>/
//	    device.age                label and name
//	    snapshot-<stamp>.age      everything this device knows
//	    changes-<stamp>.age       what changed since its previous file
//
// Each device writes and deletes only files in its own directory.

type formatFile struct {
	Format  string    `json:"format"`
	Set     string    `json:"set"`
	KeyID   string    `json:"key_id"`
	Created time.Time `json:"created"`
}

type folder struct {
	root string // .../holocron-sync
}

func (f folder) formatPath() string          { return filepath.Join(f.root, "format.json") }
func (f folder) keysDir() string             { return filepath.Join(f.root, "keys") }
func (f folder) devicesDir() string          { return filepath.Join(f.root, "devices") }
func (f folder) deviceDir(uid string) string { return filepath.Join(f.devicesDir(), uid) }

func (f folder) readFormat() (formatFile, error) {
	var ff formatFile
	data, err := os.ReadFile(f.formatPath())
	if errors.Is(err, fs.ErrNotExist) {
		return ff, fmt.Errorf("%s is not a Holocron sync folder (no %s)", filepath.Dir(f.root), filepath.Join(DirName, "format.json"))
	}
	if err != nil {
		return ff, err
	}
	if err := json.Unmarshal(data, &ff); err != nil {
		return ff, fmt.Errorf("reading %s: %w", f.formatPath(), err)
	}
	if ff.Format != FormatVersion {
		return ff, fmt.Errorf("the sync folder uses format %q, which this Holocron does not understand; upgrade Holocron", ff.Format)
	}
	return ff, nil
}

func (f folder) writeFormat(ff formatFile) error {
	data, err := json.MarshalIndent(ff, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(f.formatPath(), append(data, '\n'))
}

// writeAtomic writes under a temporary name and renames, so a sync tool
// never uploads a half-written file.
func writeAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	tmp := filepath.Join(filepath.Dir(path), ".tmp-"+hex.EncodeToString(b[:]))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// A record file's name carries a stamp that orders a device's files.
type recordFile struct {
	name     string
	snapshot bool
	stamp    int64
}

func parseRecordFile(name string) (recordFile, bool) {
	base, ok := strings.CutSuffix(name, ".age")
	if !ok {
		return recordFile{}, false
	}
	kind, stamp, ok := strings.Cut(base, "-")
	if !ok || (kind != "snapshot" && kind != "changes") {
		return recordFile{}, false
	}
	n, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil {
		return recordFile{}, false
	}
	return recordFile{name: name, snapshot: kind == "snapshot", stamp: n}, true
}

func (rf recordFile) time() time.Time { return time.UnixMilli(rf.stamp) }

// listRecordFiles returns a device's record files, oldest first.
func (f folder) listRecordFiles(uid string) ([]recordFile, error) {
	ents, err := os.ReadDir(f.deviceDir(uid))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []recordFile
	for _, e := range ents {
		if rf, ok := parseRecordFile(e.Name()); ok {
			out = append(out, rf)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].stamp < out[j].stamp })
	return out, nil
}

// deviceUIDs lists the devices with a directory in the folder.
func (f folder) deviceUIDs() ([]string, error) {
	ents, err := os.ReadDir(f.devicesDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && journal.IsUID(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// nextStamp returns a stamp later than any file this device has written.
func (f folder) nextStamp(uid string, now time.Time) (int64, error) {
	files, err := f.listRecordFiles(uid)
	if err != nil {
		return 0, err
	}
	stamp := now.UnixMilli()
	if n := len(files); n > 0 && files[n-1].stamp >= stamp {
		stamp = files[n-1].stamp + 1
	}
	return stamp, nil
}

func recordFileName(snapshot bool, stamp int64) string {
	kind := "changes"
	if snapshot {
		kind = "snapshot"
	}
	return fmt.Sprintf("%s-%016d.age", kind, stamp)
}

// writeRecords compresses, encrypts and writes records.
func writeRecords(path string, recs journal.Records, key *age.HybridIdentity) error {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(recs); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	data, err := encryptBytes(buf.Bytes(), key.Recipient())
	if err != nil {
		return err
	}
	return writeAtomic(path, data)
}

// readRecords decrypts and decompresses a record file.
func readRecords(path string, keys []age.Identity) (journal.Records, error) {
	var recs journal.Records
	data, err := os.ReadFile(path)
	if err != nil {
		return recs, err
	}
	plain, err := decryptBytes(data, keys...)
	if err != nil {
		var noMatch *age.NoIdentityMatchError
		if errors.As(err, &noMatch) {
			return recs, fmt.Errorf("%s was encrypted with a sync key this computer does not have (was the key changed? run `holocron sync unlock`): %w", filepath.Base(path), ErrLocked)
		}
		return recs, fmt.Errorf("%s is damaged or was altered: %w", filepath.Base(path), err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return recs, err
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return recs, err
	}
	return recs, json.Unmarshal(raw, &recs)
}

func writeDevice(path string, d journal.DeviceRecord, key *age.HybridIdentity) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	enc, err := encryptBytes(data, key.Recipient())
	if err != nil {
		return err
	}
	return writeAtomic(path, enc)
}

func readDevice(path string, keys []age.Identity) (journal.DeviceRecord, error) {
	var d journal.DeviceRecord
	data, err := os.ReadFile(path)
	if err != nil {
		return d, err
	}
	plain, err := decryptBytes(data, keys...)
	var noMatch *age.NoIdentityMatchError
	if errors.As(err, &noMatch) {
		return d, fmt.Errorf("the sync key was changed on another computer; unlock again with `holocron sync unlock`: %w", ErrLocked)
	}
	if err != nil {
		return d, fmt.Errorf("reading %s: %w", path, err)
	}
	return d, json.Unmarshal(plain, &d)
}
