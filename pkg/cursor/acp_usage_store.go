package cursor

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	acp "github.com/JieWaZi/acp-go-sdk"
	sqlite3 "github.com/ncruces/go-sqlite3"
)

// cursorACPUsageIdentity retains only the authoritative turn's request and user-input identities.
type cursorACPUsageIdentity struct {
	// requestID is the request_id of the AgentTurn referenced by the latest root.
	requestID string
	// userID is the user_message_id from that same AgentTurn.
	userID string
}

// readCursorACPUsageIdentity follows metadata, the root, and the latest turn while preserving visibility of native WAL writes.
func readCursorACPUsageIdentity(ctx context.Context, configRoot string, session acp.SessionId) (cursorACPUsageIdentity, error) {
	var identity cursorACPUsageIdentity
	if !filepath.IsAbs(configRoot) || !validCursorUsageID(string(session)) {
		return identity, errors.New("invalid Cursor ACP usage store scope")
	}
	path := filepath.Join(configRoot, "acp-sessions", string(session), "store.db")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return identity, nil
	}
	if err != nil {
		return identity, err
	}
	if !info.Mode().IsRegular() {
		return identity, errors.New("Cursor ACP usage store must be a regular file")
	}
	u := url.URL{Scheme: "file", Path: path}
	query := u.Query()
	query.Set("mode", "ro")
	u.RawQuery = query.Encode()
	db, err := sqlite3.OpenContext(ctx, u.String())
	if err != nil {
		return identity, err
	}
	defer db.Close()
	db.SetInterrupt(ctx)
	if err := db.Exec("BEGIN"); err != nil {
		return identity, err
	}
	defer db.Exec("ROLLBACK")
	metadata, _, err := db.Prepare("SELECT length(value),value FROM meta WHERE key='0'")
	if err != nil {
		return identity, err
	}
	defer metadata.Close()
	if !metadata.Step() {
		return identity, metadata.Err()
	}
	if metadata.ColumnInt64(0) <= 0 || metadata.ColumnInt64(0) > 2<<20 {
		return identity, errors.New("Cursor ACP metadata exceeds usage proof limit")
	}
	raw, err := hex.DecodeString(metadata.ColumnText(1))
	if err != nil {
		return identity, errors.New("invalid Cursor ACP usage metadata")
	}
	var meta struct {
		// Session is the native session identity recorded in official metadata.
		Session string `json:"agentId"`
		// Root is the hexadecimal identity of the authoritative root blob.
		Root string `json:"latestRootBlobId"`
	}
	if json.Unmarshal(raw, &meta) != nil || meta.Session != string(session) {
		return identity, errors.New("Cursor ACP usage metadata has a different session")
	}
	if meta.Root == "" {
		return identity, nil
	}
	rootID, err := hex.DecodeString(meta.Root)
	if err != nil {
		return identity, errors.New("invalid Cursor ACP usage root identity")
	}
	root, err := cursorACPUsageBlob(db, rootID)
	if err != nil {
		return identity, err
	}
	lastID, err := cursorACPUsageField(root, 8)
	if err != nil || lastID == nil {
		return identity, err
	}
	wrapper, err := cursorACPUsageBlob(db, lastID)
	if err != nil {
		return identity, err
	}
	turn, err := cursorACPUsageField(wrapper, 1)
	if err != nil || turn == nil {
		return identity, err
	}
	request, err := cursorACPUsageField(turn, 3)
	if err != nil {
		return identity, err
	}
	user, err := cursorACPUsageField(turn, 10)
	if err != nil {
		return identity, err
	}
	identity.requestID, identity.userID = string(request), string(user)
	if !validCursorUsageID(identity.requestID) || !validCursorUsageID(identity.userID) {
		return cursorACPUsageIdentity{}, errors.New("invalid Cursor ACP authoritative usage identity")
	}
	userBlobID, err := cursorACPUsageField(turn, 1)
	if err != nil {
		return cursorACPUsageIdentity{}, err
	}
	if userBlobID != nil {
		userBlob, err := cursorACPUsageBlob(db, userBlobID)
		if err != nil {
			return cursorACPUsageIdentity{}, err
		}
		nativeUser, err := cursorACPUsageField(userBlob, 2)
		if err != nil || nativeUser != nil && string(nativeUser) != identity.userID {
			return cursorACPUsageIdentity{}, errors.New("Cursor ACP authoritative user identities disagree")
		}
	}
	return identity, nil
}

func cursorACPUsageBlob(db *sqlite3.Conn, id []byte) ([]byte, error) {
	if len(id) != sha256.Size {
		return nil, errors.New("invalid Cursor ACP usage blob identity")
	}
	statement, _, err := db.Prepare("SELECT length(data),data FROM blobs WHERE id=?")
	if err != nil {
		return nil, err
	}
	defer statement.Close()
	if err := statement.BindText(1, hex.EncodeToString(id)); err != nil {
		return nil, err
	}
	if !statement.Step() {
		if err := statement.Err(); err != nil {
			return nil, err
		}
		return nil, errors.New("Cursor ACP authoritative usage blob is missing")
	}
	if size := statement.ColumnInt64(0); size < 0 || size > 8<<20 {
		return nil, errors.New("Cursor ACP usage blob exceeds proof limit")
	}
	return statement.ColumnBlob(1, nil), nil
}

// cursorACPUsageField extracts one length-delimited field without recursively parsing message or tool content.
func cursorACPUsageField(data []byte, wanted uint64) ([]byte, error) {
	if len(data) > 8<<20 {
		return nil, errors.New("Cursor ACP usage proof exceeds retained size")
	}
	var result []byte
	for count := 0; len(data) > 0; count++ {
		if count >= 32768 {
			return nil, errors.New("Cursor ACP usage proof has too many fields")
		}
		key, n := binary.Uvarint(data)
		if n <= 0 || key>>3 == 0 {
			return nil, errors.New("invalid Cursor ACP usage proof field")
		}
		data = data[n:]
		switch key & 7 {
		case 0:
			_, n := binary.Uvarint(data)
			if n <= 0 {
				return nil, errors.New("invalid Cursor ACP usage proof integer")
			}
			data = data[n:]
		case 1, 5:
			size := 8
			if key&7 == 5 {
				size = 4
			}
			if len(data) < size {
				return nil, errors.New("truncated Cursor ACP usage proof scalar")
			}
			data = data[size:]
		case 2:
			size, n := binary.Uvarint(data)
			if n <= 0 || size > uint64(len(data)-n) {
				return nil, errors.New("truncated Cursor ACP usage proof bytes")
			}
			data = data[n:]
			if key>>3 == wanted {
				result = data[:int(size)]
			}
			data = data[int(size):]
		default:
			return nil, errors.New("unsupported Cursor ACP usage proof wire type")
		}
	}
	return result, nil
}
