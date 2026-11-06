package standardacp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	acp "github.com/JieWaZi/acp-go-sdk"
)

var (
	geminiNativeSessionID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	geminiProjectSlug     = regexp.MustCompile(`^[a-z0-9-]+$`)
)

const maxGeminiTranscript = 64 << 20

// prepareGeminiRestore protects the native v0.62.0 recorder's minute-based
// filename before session/load. Native selection reads history before the first
// chat initialization, but that initialization appends startup state to the
// same path. A sorted-first, byte-for-byte sibling wins the native selector's
// lastUpdated tie and gives the resumed recorder a distinct writable path.
// Source: gemini-cli b460678f3db508407554afd604cc9d6635becb2a,
// acpSessionManager.ts, sessionUtils.ts, and chatRecordingService.ts.
func prepareGeminiRestore(ctx context.Context, environment []string, cwd string, id acp.SessionId) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !geminiNativeSessionID.MatchString(string(id)) {
		return nil
	}
	home := environmentValue(environment, "GEMINI_CLI_HOME")
	if home == "" {
		homeKey := "HOME"
		if runtime.GOOS == "windows" {
			homeKey = "USERPROFILE"
		}
		home = environmentValue(environment, homeKey)
	}
	if home == "" {
		account, err := user.Current()
		if err != nil || account.HomeDir == "" {
			return errors.New("Gemini restore could not resolve the native account home")
		}
		home = account.HomeDir
	}
	if !filepath.IsAbs(home) {
		home = filepath.Join(cwd, home)
	}
	if environmentValue(environment, "SANDBOX") == "sandbox-exec" {
		home = filepath.Join(home, ".cache")
	}
	profile, err := os.OpenRoot(filepath.Join(home, ".gemini"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("Gemini restore could not access the native profile: %w", err)
	}
	defer profile.Close()

	info, err := profile.Lstat("tmp")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() {
		return errors.New("Gemini restore could not verify the native runtime directory")
	}
	projectHash := fmt.Sprintf("%x", sha256.Sum256([]byte(cwd)))
	project, err := geminiRestoreProject(profile, cwd, projectHash)
	if err != nil {
		return err
	}
	for _, path := range []string{filepath.Join("tmp", project), filepath.Join("tmp", project, "chats")} {
		info, err := profile.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || !info.IsDir() {
			return errors.New("Gemini restore could not verify the native chat directory")
		}
	}
	chats, err := profile.OpenRoot(filepath.Join("tmp", project, "chats"))
	if err != nil {
		return err
	}
	defer chats.Close()
	now := time.Now().UTC().Truncate(time.Minute)
	// The ACP request is bounded to 30 seconds. Include adjacent minute
	// filenames so a rollover between preflight and initialization is safe.
	for _, offset := range []time.Duration{-time.Minute, 0, time.Minute} {
		name := "session-" + now.Add(offset).Format("2006-01-02T15-04") + "-" + string(id[:8]) + ".jsonl"
		if err := protectGeminiTranscript(ctx, chats, name, id, projectHash); err != nil {
			return err
		}
	}
	return nil
}

func geminiRestoreProject(profile *os.Root, cwd, hash string) (string, error) {
	key := geminiRegistryPath(cwd)
	var registry struct {
		// Projects maps the normalized native project path to its directory slug.
		Projects map[string]string `json:"projects"`
	}
	_, raw, err := readGeminiFile(profile, "projects.json", 1<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("Gemini restore could not read the native project registry: %w", err)
	}
	if err == nil && json.Unmarshal(raw, &registry) != nil {
		return "", errors.New("Gemini restore could not decode the native project registry")
	}
	slug := registry.Projects[key]
	if slug == "" {
		slug, err = findGeminiProject(profile, key, hash)
		if err != nil {
			return "", err
		}
		if slug == "" {
			return hash, nil
		}
	}
	if !geminiProjectSlug.MatchString(slug) {
		return "", errors.New("Gemini restore found an invalid native project slug")
	}
	for _, base := range []string{"tmp", "history"} {
		owner, err := geminiProjectOwner(profile, base, slug)
		if err != nil {
			return "", err
		}
		if owner != "" && owner != key {
			return "", errors.New("Gemini restore project directory has a different ownership marker")
		}
	}
	dir, err := profile.Open(filepath.Join("tmp", slug))
	if errors.Is(err, os.ErrNotExist) {
		return hash, nil
	}
	if err != nil {
		return "", err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	// StorageMigration copies from the old hash only while the slug directory
	// is fresh. Once it holds project state, only the slug is used by Load.
	if len(names) == 0 || len(names) == 1 && names[0] == ".project_root" {
		return hash, nil
	}
	return slug, nil
}

func findGeminiProject(profile *os.Root, cwd, hash string) (string, error) {
	// Gemini can rebuild a missing registry from existing .project_root markers.
	// Limit inspection to direct children of the one selected profile.
	tmp, err := profile.Open("tmp")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	entries, err := tmp.ReadDir(4097)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if len(entries) > 4096 {
		return "", errors.New("Gemini restore project registry is missing and the project search is too large")
	}
	var slug string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || name == hash || !geminiProjectSlug.MatchString(name) {
			continue
		}
		owner, err := geminiProjectOwner(profile, "tmp", name)
		if err != nil {
			return "", err
		}
		if owner == cwd {
			if slug != "" {
				return "", errors.New("Gemini restore found multiple native directories for the same project")
			}
			slug = name
		}
	}
	return slug, nil
}

func geminiRegistryPath(value string) string {
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return filepath.Clean(value)
}

func geminiProjectOwner(profile *os.Root, base, slug string) (string, error) {
	dir := filepath.Join(base, slug)
	info, err := profile.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.IsDir() {
		return "", errors.New("Gemini restore could not verify the native project directory")
	}
	_, marker, err := readGeminiFile(profile, filepath.Join(dir, ".project_root"), 4096)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(marker))
	if !filepath.IsAbs(value) {
		return "", errors.New("Gemini restore found an invalid native project ownership marker")
	}
	return geminiRegistryPath(value), nil
}

func protectGeminiTranscript(ctx context.Context, chats *os.Root, name string, id acp.SessionId, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, original, err := readGeminiFile(chats, name, maxGeminiTranscript)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("Gemini restore could not read the colliding transcript: %w", err)
	}
	current, err := inspectGeminiTranscript(original, id, hash)
	if err != nil {
		return err
	}
	if !current.resumable {
		return nil
	}

	backup := "session-!acp-go-" + strings.TrimPrefix(name, "session-")
	if _, saved, err := readGeminiFile(chats, backup, maxGeminiTranscript); err == nil {
		prior, err := inspectGeminiTranscript(saved, id, hash)
		if err != nil {
			return err
		}
		if bytes.Equal(saved, original) || prior.resumable && !current.updated.IsZero() && prior.updated.After(current.updated) {
			return nil
		}
		return errors.New("Gemini restore cannot safely select between differing colliding transcripts; retry after the recording minute")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	staged := ".acp-go-transcript-" + rand.Text()
	file, err := chats.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer chats.Remove(staged)
	_, writeErr := file.Write(original)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	check, err := chats.Lstat(name)
	if err != nil || !sameGeminiFile(info, check) {
		return errors.New("Gemini restore transcript changed during preparation")
	}
	// Publish the completed file without overwriting any concurrent writer or
	// exposing an incomplete transcript to native listing.
	if err := chats.Link(staged, backup); err != nil {
		return fmt.Errorf("Gemini restore could not publish the protected transcript: %w", err)
	}
	return nil
}

func readGeminiFile(root *os.Root, name string, maxSize int64) (os.FileInfo, []byte, error) {
	entry, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !entry.Mode().IsRegular() || entry.Size() > maxSize {
		return nil, nil, errors.New("Gemini restore found an unsafe or oversized native file")
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !sameGeminiFile(entry, info) {
		return nil, nil, errors.New("Gemini restore native file changed before reading")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
	if err != nil {
		return nil, nil, err
	}
	check, err := file.Stat()
	if err != nil || int64(len(data)) != info.Size() || !sameGeminiFile(info, check) {
		return nil, nil, errors.New("Gemini restore native file changed during reading")
	}
	return info, data, nil
}

func sameGeminiFile(before, after os.FileInfo) bool {
	return os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

type geminiTranscriptState struct {
	// resumable reports whether the native selector could use this conversation.
	resumable bool
	// updated is the native lastUpdated value used for duplicate selection.
	updated time.Time
}

type geminiTranscriptLine struct {
	// SessionID identifies the native conversation owner.
	SessionID string `json:"sessionId"`
	// ProjectHash identifies the workspace that owns the transcript.
	ProjectHash string `json:"projectHash"`
	// Kind distinguishes root sessions from tool-owned subagents.
	Kind string `json:"kind"`
	// LastUpdated is the native selector's duplicate tie-breaker.
	LastUpdated string `json:"lastUpdated"`
	// Rewind trims the conversation starting with a specific recorded message.
	Rewind string `json:"$rewindTo"`
	// Set applies a metadata patch, including an optional full message reset.
	Set *geminiTranscriptLine `json:"$set"`
	// Messages can be supplied by a full record or a native checkpoint.
	Messages *[]geminiTranscriptMessage `json:"messages"`
	// geminiTranscriptMessage holds the content of ordinary message records.
	geminiTranscriptMessage
}

type geminiTranscriptMessage struct {
	// ID identifies the stored message.
	ID string `json:"id"`
	// Type is the native message role.
	Type string `json:"type"`
	// Content is either native text or a GenerateContent parts array.
	Content json.RawMessage `json:"content"`
	// Tools are calls recorded with model output.
	Tools []json.RawMessage `json:"toolCalls"`
	// Thoughts are native reasoning summaries attached to model output.
	Thoughts []json.RawMessage `json:"thoughts"`
}

func inspectGeminiTranscript(data []byte, id acp.SessionId, hash string) (geminiTranscriptState, error) {
	var updated time.Time
	var messageOrder []string
	messages := make(map[string]bool)
	addMessage := func(message geminiTranscriptMessage) {
		if message.ID == "" {
			return
		}
		if _, exists := messages[message.ID]; !exists {
			messageOrder = append(messageOrder, message.ID)
		}
		messages[message.ID] = message.resumable()
	}
	first := true
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var record geminiTranscriptLine
		if err := json.Unmarshal(line, &record); err != nil {
			return geminiTranscriptState{}, errors.New("Gemini restore found malformed native transcript data")
		}
		patch := record.Set != nil && record.ID == "" && record.Rewind == ""
		if patch {
			record = *record.Set
		}
		if first && (record.SessionID == "" || record.ProjectHash == "") ||
			record.SessionID != "" && record.SessionID != string(id) ||
			record.ProjectHash != "" && record.ProjectHash != hash ||
			record.Kind != "" && record.Kind != "main" {
			return geminiTranscriptState{}, errors.New("Gemini restore transcript does not match the exact native session and project")
		}
		first = false
		if record.LastUpdated != "" {
			updated, _ = time.Parse(time.RFC3339Nano, record.LastUpdated)
		}
		switch {
		case record.Rewind != "":
			index := 0
			for i, id := range messageOrder {
				if id == record.Rewind {
					index = i
					break
				}
			}
			for _, id := range messageOrder[index:] {
				delete(messages, id)
			}
			messageOrder = messageOrder[:index]
		case record.ID != "":
			addMessage(record.geminiTranscriptMessage)
		case record.Messages != nil:
			if patch {
				clear(messages)
				messageOrder = messageOrder[:0]
			}
			for _, message := range *record.Messages {
				addMessage(message)
			}
		}
	}
	if first {
		return geminiTranscriptState{}, errors.New("Gemini restore could not confirm the native transcript identity")
	}
	for _, substantive := range messages {
		if substantive {
			return geminiTranscriptState{resumable: true, updated: updated}, nil
		}
	}
	return geminiTranscriptState{}, nil
}

func (message geminiTranscriptMessage) resumable() bool {
	text := strings.TrimSpace(geminiPartText(message.Content))
	switch message.Type {
	case "user":
		return text != "" && !strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "?") &&
			!strings.HasPrefix(text, "<session_context>") && !strings.HasPrefix(text, "<hook_context>")
	case "gemini":
		return text != "" || len(message.Tools) != 0 || len(message.Thoughts) != 0
	default:
		return false
	}
}

// Gemini's verbose part rendering treats media and tool parts as content too.
// The preflight only needs their presence and the leading text classification.
func geminiPartText(value json.RawMessage) string {
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	var list []json.RawMessage
	if json.Unmarshal(value, &list) == nil {
		var combined strings.Builder
		for _, part := range list {
			combined.WriteString(geminiPartText(part))
		}
		return combined.String()
	}
	var part map[string]json.RawMessage
	if json.Unmarshal(value, &part) != nil {
		return ""
	}
	for _, key := range []string{"videoMetadata", "thought", "codeExecutionResult", "executableCode", "fileData", "functionCall", "functionResponse", "inlineData"} {
		if _, present := part[key]; present {
			return "[native part]"
		}
	}
	_ = json.Unmarshal(part["text"], &text)
	return text
}
