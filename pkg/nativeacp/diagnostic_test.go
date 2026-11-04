package nativeacp

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestDiagnosticRedactsAcrossChunks(t *testing.T) {
	var log bytes.Buffer
	writer := &diagnosticWriter{logger: slog.New(slog.NewTextHandler(&log, nil)), redact: func(s string) string { return strings.ReplaceAll(s, "secret-token", "[redacted]") }}
	_, _ = writer.Write([]byte("failed: secret-"))
	_, _ = writer.Write([]byte("token\n"))
	if strings.Contains(log.String()+writer.String(), "secret-") || !strings.Contains(writer.String(), "[redacted]") {
		t.Fatalf("unsafe diagnostic: log=%s tail=%s", log.String(), writer.String())
	}
}

func TestDiagnosticDropsOverflowAndSuppressesWithoutRedactor(t *testing.T) {
	var log bytes.Buffer
	writer := &diagnosticWriter{logger: slog.New(slog.NewTextHandler(&log, nil)), redact: func(s string) string { return s }}
	_, _ = writer.Write([]byte(strings.Repeat("x", maxNativeDiagnosticRecordBytes+1)))
	_, _ = writer.Write([]byte("secret-tail\nsafe\n"))
	if strings.Contains(log.String()+writer.String(), "secret-tail") || !strings.Contains(writer.String(), "safe") {
		t.Fatal("overflow tail became a new diagnostic line")
	}
	writer.redact = nil
	_, _ = writer.Write([]byte("unconfigured-secret\n"))
	if strings.Contains(log.String()+writer.String(), "unconfigured-secret") {
		t.Fatal("unconfigured diagnostic leaked")
	}
}

func TestDiagnosticPreservesStructuredRedactionContext(t *testing.T) {
	var records []string
	writer := &diagnosticWriter{redact: func(record string) string {
		records = append(records, record)
		if strings.Contains(record, `"credentials"`) {
			return "[redacted payload]"
		}
		return record
	}}
	for _, chunk := range []string{"error: {\n  \"credentials\": {\n", "    \"value\": \"secret\"\n  }\n}\n", "apiKey=\x1b[", "31msecret\x1b[0m\n"} {
		_, _ = writer.Write([]byte(chunk))
	}
	if len(records) != 2 || !strings.Contains(records[0], `"value": "secret"`) || strings.Contains(records[1], "\x1b") {
		t.Fatalf("record context lost: %#v", records)
	}
	if strings.Contains(writer.String(), `"value"`) {
		t.Fatal("structured secret escaped redactor")
	}
}
