package cursor

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	acp "github.com/JieWaZi/acp-go-sdk"
	_ "github.com/ncruces/go-sqlite3/driver"
)

const (
	usageTestSession    = "33b71216-7e10-47c9-b423-997ec7209ff2"
	usageTestRequest    = "76fd5245-39da-4e2d-9852-496601581037"
	usageTestUser       = "592bf869-947c-47be-9c71-70314ccaa833"
	usageTestOldRequest = "16fd5245-39da-4e2d-9852-496601581037"
	usageTestOldUser    = "192bf869-947c-47be-9c71-70314ccaa833"
)

func usageTestDB(t *testing.T) (*sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "acp-sessions", usageTestSession)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(directory, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{"PRAGMA journal_mode=WAL", "CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT)", "CREATE TABLE blobs(id TEXT PRIMARY KEY,data BLOB)"} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	return db, root
}

func usageTestBytes(field int, value []byte) []byte {
	data := binary.AppendUvarint(nil, uint64(field<<3|2))
	data = binary.AppendUvarint(data, uint64(len(value)))
	return append(data, value...)
}

func usageTestTurn(t *testing.T, db *sql.DB, request, user string) {
	t.Helper()
	put := func(label string, data []byte) []byte {
		id := sha256.Sum256([]byte(label))
		if _, err := db.Exec("INSERT OR REPLACE INTO blobs VALUES (?,?)", hex.EncodeToString(id[:]), data); err != nil {
			t.Fatal(err)
		}
		return id[:]
	}
	userBlob := put("user:"+user, usageTestBytes(2, []byte(user)))
	turn := usageTestBytes(1, userBlob)
	turn = append(turn, usageTestBytes(3, []byte(request))...)
	turn = append(turn, usageTestBytes(10, []byte(user))...)
	turnBlob := put("turn:"+request, usageTestBytes(1, turn))
	rootBlob := put("root:"+request, usageTestBytes(8, turnBlob))
	meta, _ := json.Marshal(map[string]string{"agentId": usageTestSession, "latestRootBlobId": hex.EncodeToString(rootBlob)})
	if _, err := db.Exec("INSERT OR REPLACE INTO meta VALUES ('0',?)", hex.EncodeToString(meta)); err != nil {
		t.Fatal(err)
	}
}

func TestCursorACPUsageIdentityReadsOnlyAuthoritativeRootAndWAL(t *testing.T) {
	db, root := usageTestDB(t)
	usageTestTurn(t, db, usageTestOldRequest, usageTestOldUser)
	before, err := readCursorACPUsageIdentity(t.Context(), root, acp.SessionId(usageTestSession))
	if err != nil || before.requestID != usageTestOldRequest || before.userID != usageTestOldUser {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	usageTestTurn(t, db, usageTestRequest, usageTestUser)
	if _, err := db.Exec("INSERT INTO blobs VALUES ('unreferenced',?)", []byte{0xff, 0xff}); err != nil {
		t.Fatal(err)
	}
	after, err := readCursorACPUsageIdentity(t.Context(), root, acp.SessionId(usageTestSession))
	if err != nil || after.requestID != usageTestRequest || after.userID != usageTestUser {
		t.Fatalf("after=%+v err=%v", after, err)
	}
}

func TestCursorACPUsageIdentityRejectsForeignAccountStoreMetadata(t *testing.T) {
	db, root := usageTestDB(t)
	usageTestTurn(t, db, usageTestRequest, usageTestUser)
	meta, _ := json.Marshal(map[string]string{"agentId": "foreign", "latestRootBlobId": "00"})
	if _, err := db.Exec("UPDATE meta SET value=? WHERE key='0'", hex.EncodeToString(meta)); err != nil {
		t.Fatal(err)
	}
	if _, err := readCursorACPUsageIdentity(t.Context(), root, acp.SessionId(usageTestSession)); err == nil {
		t.Fatal("foreign store was accepted")
	}
}

func TestCursorACPUsageIdentityRejectsDifferentNativeUserBlob(t *testing.T) {
	db, root := usageTestDB(t)
	usageTestTurn(t, db, usageTestRequest, usageTestUser)
	id := sha256.Sum256([]byte("user:" + usageTestUser))
	if _, err := db.Exec("UPDATE blobs SET data=? WHERE id=?", usageTestBytes(2, []byte(usageTestOldUser)), hex.EncodeToString(id[:])); err != nil {
		t.Fatal(err)
	}
	if _, err := readCursorACPUsageIdentity(t.Context(), root, acp.SessionId(usageTestSession)); err == nil {
		t.Fatal("mismatched native user blob was accepted")
	}
}
