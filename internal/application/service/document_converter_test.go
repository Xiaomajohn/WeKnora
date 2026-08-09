package service

import (
	"archive/zip"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testUploadHeader(t *testing.T, name string, content []byte) (*multipart.FileHeader, func()) {
	t.Helper()
	file, cleanup, err := multipartFileHeaderFromBytes(name, content)
	if err != nil {
		t.Fatalf("create multipart file: %v", err)
	}
	return file, cleanup
}

func testDOCXBytes(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, name := range requiredDocxEntries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = entry.Write([]byte("content")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestPreprocessLegacyDocUploadKeepsRealDOC(t *testing.T) {
	content := append(append([]byte(nil), legacyDocMagic...), []byte("payload")...)
	file, cleanup := testUploadHeader(t, "real.doc", content)
	defer cleanup()

	result, err := preprocessLegacyDocUpload(context.Background(), file, "real.doc")
	if err != nil {
		t.Fatalf("preprocess failed: %v", err)
	}
	if result.file != file || result.fileName != "real.doc" {
		t.Fatalf("real DOC was unexpectedly replaced: %#v", result)
	}
}

func TestPreprocessLegacyDocUploadRejectsUnknownPayload(t *testing.T) {
	file, cleanup := testUploadHeader(t, "fake.doc", []byte("not a word document"))
	defer cleanup()

	if _, err := preprocessLegacyDocUpload(context.Background(), file, "fake.doc"); err == nil {
		t.Fatal("expected mismatched DOC payload to be rejected")
	}
}

func TestPreprocessLegacyDocUploadConvertsMHTML(t *testing.T) {
	docx := testDOCXBytes(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/convert/mhtml-to-docx" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse request: %v", err)
		}
		w.Header().Set("Content-Type", documentConverterDocxMIME)
		_, _ = w.Write(docx)
	}))
	defer server.Close()
	t.Setenv("DOCUMENT_CONVERTER_URL", server.URL)

	mhtml := []byte("MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=abc\r\n\r\n--abc\r\nContent-Type: text/html\r\n\r\n<html><body>中文</body></html>\r\n--abc--\r\n")
	file, cleanup := testUploadHeader(t, "mail.DOC", mhtml)
	defer cleanup()

	result, err := preprocessLegacyDocUpload(context.Background(), file, "mail.DOC")
	if err != nil {
		t.Fatalf("preprocess failed: %v", err)
	}
	defer result.cleanup()
	if result.fileName != "mail.docx" {
		t.Fatalf("unexpected converted filename: %s", result.fileName)
	}
	converted, err := readMultipartFile(result.file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(converted, docx) {
		t.Fatal("converted bytes differ from service response")
	}
}

func TestCallDocumentConverterRejectsInvalidDOCX(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", documentConverterDocxMIME)
		_, _ = w.Write([]byte("not a docx"))
	}))
	defer server.Close()
	t.Setenv("DOCUMENT_CONVERTER_URL", server.URL)

	if _, err := callDocumentConverter(context.Background(), []byte("mime"), "mail.doc"); err == nil {
		t.Fatal("expected invalid DOCX response to be rejected")
	}
}

func TestCallDocumentConverterRequiresURL(t *testing.T) {
	t.Setenv("DOCUMENT_CONVERTER_URL", "")
	if _, err := callDocumentConverter(context.Background(), []byte("mime"), "mail.doc"); err == nil {
		t.Fatal("expected empty DOCUMENT_CONVERTER_URL to be rejected")
	}
}

func TestCallDocumentConverterRejectsWrongContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":"bad"}`))
	}))
	defer server.Close()
	t.Setenv("DOCUMENT_CONVERTER_URL", server.URL)

	if _, err := callDocumentConverter(context.Background(), []byte("mime"), "mail.doc"); err == nil {
		t.Fatal("expected wrong content-type to be rejected")
	}
}

func TestCallDocumentConverterMapsServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"conversion_failed","message":"soffice crashed"}`))
	}))
	defer server.Close()
	t.Setenv("DOCUMENT_CONVERTER_URL", server.URL)

	if _, err := callDocumentConverter(context.Background(), []byte("mime"), "mail.doc"); err == nil {
		t.Fatal("expected server error to be propagated")
	}
}

func TestCallDocumentConverterMapsBusy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"busy","message":"service busy"}`))
	}))
	defer server.Close()
	t.Setenv("DOCUMENT_CONVERTER_URL", server.URL)

	if _, err := callDocumentConverter(context.Background(), []byte("mime"), "mail.doc"); err == nil {
		t.Fatal("expected busy response to be propagated")
	}
}

func TestCallDocumentConverterRejectsOversizedResponse(t *testing.T) {
	t.Setenv("DOCUMENT_CONVERTER_MAX_RESPONSE_MB", "1")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", documentConverterDocxMIME)
		_, _ = w.Write(bytes.Repeat([]byte("A"), 2*1024*1024))
	}))
	defer server.Close()
	t.Setenv("DOCUMENT_CONVERTER_URL", server.URL)

	if _, err := callDocumentConverter(context.Background(), []byte("mime"), "mail.doc"); err == nil {
		t.Fatal("expected oversized response to be rejected")
	}
}

func TestCallDocumentConverterRejectsRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://attacker.example/leak", http.StatusFound)
	}))
	defer server.Close()
	t.Setenv("DOCUMENT_CONVERTER_URL", server.URL)

	if _, err := callDocumentConverter(context.Background(), []byte("mime"), "mail.doc"); err == nil {
		t.Fatal("expected redirect to be rejected")
	}
}

func TestPreprocessLegacyDocUploadRequiresServiceForMHTML(t *testing.T) {
	t.Setenv("DOCUMENT_CONVERTER_URL", "")

	mhtml := []byte("MIME-Version: 1.0\r\nContent-Type: multipart/related; boundary=abc\r\n\r\n--abc\r\nContent-Type: text/html\r\n\r\n<html><body>x</body></html>\r\n--abc--\r\n")
	file, cleanup := testUploadHeader(t, "mail.doc", mhtml)
	defer cleanup()

	if _, err := preprocessLegacyDocUpload(context.Background(), file, "mail.doc"); err == nil {
		t.Fatal("expected unconfigured converter service to be rejected")
	}
}

func TestLooksLikeMHTML(t *testing.T) {
	cases := map[string][]byte{
		"plain text":          []byte("hello world"),
		"missing boundary":    []byte("Content-Type: multipart/related"),
		"missing type":        []byte("boundary=abc"),
		"valid mime/related":  []byte("Content-Type: multipart/related; boundary=abc"),
		"uppercase headers":   []byte("CONTENT-TYPE: MULTIPART/RELATED; BOUNDARY=abc"),
		"line folded header":  []byte("Content-Type: multipart/related;\r\n boundary=abc"),
		"crlf header only":    []byte("Content-Type: multipart/related; boundary=abc\r\n\r\nbody"),
		"lf header only":      []byte("Content-Type: multipart/related; boundary=abc\n\nbody"),
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			_ = looksLikeMHTML(payload)
		})
	}
}
