package service

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	werrors "github.com/Tencent/WeKnora/internal/errors"
)

const documentConverterDocxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

var legacyDocMagic = []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}
var requiredDocxEntries = []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"}

type convertedUpload struct {
	file     *multipart.FileHeader
	fileName string
	cleanup  func()
}

type converterErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func preprocessLegacyDocUpload(ctx context.Context, file *multipart.FileHeader, fileName string) (*convertedUpload, error) {
	result := &convertedUpload{file: file, fileName: fileName, cleanup: func() {}}
	if !strings.EqualFold(filepath.Ext(fileName), ".doc") {
		return result, nil
	}

	content, err := readMultipartFile(file)
	if err != nil {
		return nil, fmt.Errorf("读取上传文件失败: %w", err)
	}
	if bytes.HasPrefix(content, legacyDocMagic) {
		return result, nil
	}
	if !looksLikeMHTML(content) {
		return nil, werrors.NewBadRequestError("文件内容与 DOC 格式不符，请上传真实 Word 文档")
	}

	converted, err := callDocumentConverter(ctx, content, fileName)
	if err != nil {
		return nil, err
	}
	convertedName := strings.TrimSuffix(fileName, filepath.Ext(fileName)) + ".docx"
	convertedFile, cleanup, err := multipartFileHeaderFromBytes(convertedName, converted)
	if err != nil {
		return nil, fmt.Errorf("创建转换后上传文件失败: %w", err)
	}
	return &convertedUpload{file: convertedFile, fileName: convertedName, cleanup: cleanup}, nil
}

func readMultipartFile(file *multipart.FileHeader) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func looksLikeMHTML(content []byte) bool {
	limit := len(content)
	if limit > 64*1024 {
		limit = 64 * 1024
	}
	head := strings.ToLower(string(content[:limit]))
	headerEnd := strings.Index(head, "\n\n")
	if crlfEnd := strings.Index(head, "\r\n\r\n"); crlfEnd >= 0 && (headerEnd < 0 || crlfEnd < headerEnd) {
		headerEnd = crlfEnd
	}
	if headerEnd >= 0 {
		head = head[:headerEnd]
	}
	return strings.Contains(head, "content-type:") &&
		strings.Contains(head, "multipart/") &&
		strings.Contains(head, "boundary=")
}

func converterTimeout() time.Duration {
	value := strings.TrimSpace(os.Getenv("DOCUMENT_CONVERTER_TIMEOUT"))
	if value == "" {
		return 60 * time.Second
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 60 * time.Second
	}
	return parsed
}

func converterMaxResponseBytes() int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("DOCUMENT_CONVERTER_MAX_RESPONSE_MB")), 10, 64)
	if err != nil || value <= 0 {
		value = 50
	}
	return value * 1024 * 1024
}

func callDocumentConverter(ctx context.Context, content []byte, fileName string) ([]byte, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("DOCUMENT_CONVERTER_URL")), "/")
	if baseURL == "" {
		return nil, werrors.NewServiceUnavailableError("检测到伪 DOC 文件，但文档转换服务未配置")
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(fileName))
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(content); err != nil {
		return nil, err
	}
	if err = writer.WriteField("filename", filepath.Base(fileName)); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, converterTimeout())
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, baseURL+"/v1/convert/mhtml-to-docx", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, werrors.NewServiceUnavailableError("文档转换服务不可用").WithDetails(err.Error())
	}
	defer resp.Body.Close()

	maxBytes := converterMaxResponseBytes()
	payload, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if readErr != nil {
		return nil, werrors.NewServiceUnavailableError("读取文档转换结果失败").WithDetails(readErr.Error())
	}
	if int64(len(payload)) > maxBytes {
		return nil, werrors.NewBadRequestError("转换后的 DOCX 文件超过大小限制")
	}
	if resp.StatusCode != http.StatusOK {
		var converterErr converterErrorResponse
		_ = json.Unmarshal(payload, &converterErr)
		message := converterErr.Message
		if message == "" {
			message = "文档转换失败"
		}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return nil, werrors.NewServiceUnavailableError(message)
		}
		return nil, werrors.NewBadRequestError(message)
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	if contentType != documentConverterDocxMIME {
		return nil, werrors.NewServiceUnavailableError("文档转换服务返回了错误的文件类型")
	}
	if err := validateDOCXBytes(payload); err != nil {
		return nil, werrors.NewServiceUnavailableError("文档转换服务返回了无效的 DOCX").WithDetails(err.Error())
	}
	return payload, nil
}

func validateDOCXBytes(content []byte) error {
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		return err
	}
	found := make(map[string]bool, len(requiredDocxEntries))
	for _, entry := range archive.File {
		found[entry.Name] = true
	}
	for _, required := range requiredDocxEntries {
		if !found[required] {
			return fmt.Errorf("missing DOCX entry %s", required)
		}
	}
	return nil
}

func multipartFileHeaderFromBytes(fileName string, content []byte) (*multipart.FileHeader, func(), error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(fileName))
	if err != nil {
		return nil, nil, err
	}
	if _, err = part.Write(content); err != nil {
		return nil, nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, nil, err
	}
	reader := multipart.NewReader(bytes.NewReader(body.Bytes()), writer.Boundary())
	form, err := reader.ReadForm(1 << 20)
	if err != nil {
		return nil, nil, err
	}
	files := form.File["file"]
	if len(files) != 1 {
		_ = form.RemoveAll()
		return nil, nil, fmt.Errorf("converted multipart file is missing")
	}
	return files[0], func() { _ = form.RemoveAll() }, nil
}
