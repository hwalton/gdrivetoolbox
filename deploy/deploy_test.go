package deploy

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// rewriteRT rewrites outgoing requests to target the test server while preserving the original path+query.
type rewriteRT struct {
	base *url.URL
	rt   http.RoundTripper
}

func (r rewriteRT) RoundTrip(req *http.Request) (*http.Response, error) {
	// clone request so we don't mutate caller's
	newReq := req.Clone(req.Context())
	newReq.URL.Scheme = r.base.Scheme
	newReq.URL.Host = r.base.Host
	// ensure URL.Path and RawQuery remain; host changed to test server
	return r.rt.RoundTrip(newReq)
}

func installTestClient(t *testing.T, srv *httptest.Server) func() {
	t.Helper()
	orig := http.DefaultClient
	u, _ := url.Parse(srv.URL)
	http.DefaultClient = &http.Client{
		Transport: rewriteRT{base: u, rt: http.DefaultTransport},
	}
	return func() { http.DefaultClient = orig }
}

func TestCheckRemoteVersionExists_MatchesAndNotMatches(t *testing.T) {
	// Handler: respond to GET query with different cases
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		// return a file only when name contains "exists.pdf"
		resp := struct {
			Files []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"files"`
		}{}
		if strings.Contains(q, "exists.pdf") {
			resp.Files = []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				Description string `json:"description"`
			}{
				{ID: "exid", Name: "exists.pdf", Description: "v1"},
			}
		}
		b, _ := json.Marshal(resp)
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	}))
	defer srv.Close()
	restore := installTestClient(t, srv)
	defer restore()

	ok, err := CheckRemoteVersionExists("token", "exists", "folder", "v1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Fatalf("expected exists -> true")
	}

	ok2, err := CheckRemoteVersionExists("token", "doesnotexist", "folder", "v1")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if ok2 {
		t.Fatalf("expected not exists -> false")
	}
}

func TestDeployPDF_NoExisting_UploadAndMove(t *testing.T) {
	// Create temp dir with dummy PDF
	td := t.TempDir()
	pdfName := "mydoc.pdf"
	if err := os.WriteFile(filepath.Join(td, pdfName), []byte("pdfdata"), 0644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	var mu sync.Mutex
	seen := []string{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()

		// Initial query (GET) should return no files
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/drive/v3/files") && r.URL.RawQuery != "uploadType=multipart" {
			// no files
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"files": []}`))
			return
		}

		// Upload endpoint
		if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/upload/drive/v3/files") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"new-file-id"}`))
			return
		}

		// Move to final folder (PATCH)
		if r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/drive/v3/files/") {
			// respond with a JSON containing id to indicate success
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"new-file-id","parents":["final"]}`))
			return
		}

		// fallback
		http.Error(w, "not implemented in test", http.StatusNotImplemented)
	}))
	defer srv.Close()
	restore := installTestClient(t, srv)
	defer restore()

	// Call DeployPDF
	err := DeployPDF("token", "mydoc", "v1", "temp", "final", "old", td)
	if err != nil {
		t.Fatalf("DeployPDF failed: %v", err)
	}

	// basic assertions about sequence
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(seen, "\n")
	if !strings.Contains(joined, "GET /drive/v3/files") {
		t.Fatalf("expected initial GET, saw: %v", joined)
	}
	if !strings.Contains(joined, "POST /upload/drive/v3/files") {
		t.Fatalf("expected upload POST, saw: %v", joined)
	}
	if !strings.Contains(joined, "PATCH /drive/v3/files/") {
		t.Fatalf("expected move PATCH, saw: %v", joined)
	}
}

func TestDeployPDF_ExistingDeletedWhenNoOldFolder(t *testing.T) {
	td := t.TempDir()
	pdfName := "existingdoc.pdf"
	if err := os.WriteFile(filepath.Join(td, pdfName), []byte("pdfdata"), 0644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	var mu sync.Mutex
	seen := []string{}

	// Simulate initial GET returning an existing file, then expect DELETE, then upload+move
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		mu.Unlock()

		// Initial query
		if r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/drive/v3/files") && r.URL.RawQuery != "uploadType=multipart" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"files":[{"id":"oldid","name":"existingdoc.pdf","description":"oldver"}]}`))
			return
		}

		// DELETE for existing file
		if r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/drive/v3/files/oldid") {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// Upload endpoint
		if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/upload/drive/v3/files") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"newid"}`))
			return
		}

		// Move to final folder (PATCH)
		if r.Method == "PATCH" && strings.HasPrefix(r.URL.Path, "/drive/v3/files/") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"newid","parents":["final"]}`))
			return
		}

		http.Error(w, "not implemented", http.StatusNotImplemented)
	}))
	defer srv.Close()
	restore := installTestClient(t, srv)
	defer restore()

	// Call DeployPDF with empty oldFolderID to trigger delete branch
	err := DeployPDF("token", "existingdoc", "v2", "temp", "final", "", td)
	if err != nil {
		t.Fatalf("DeployPDF failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(seen, "\n")
	if !strings.Contains(joined, "GET /drive/v3/files") {
		t.Fatalf("expected initial GET, saw: %v", joined)
	}
	if !strings.Contains(joined, "DELETE /drive/v3/files/oldid") {
		t.Fatalf("expected DELETE old file, saw: %v", joined)
	}
	if !strings.Contains(joined, "POST /upload/drive/v3/files") {
		t.Fatalf("expected upload POST, saw: %v", joined)
	}
}

type rewritingRoundTripper struct {
	orig       http.RoundTripper
	targetBase *url.URL
}

func (r *rewritingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	// clone request and rewrite URL scheme+host to targetBase, keep path and query
	newReq := req.Clone(req.Context())
	newURL := *req.URL
	newURL.Scheme = r.targetBase.Scheme
	newURL.Host = r.targetBase.Host
	newReq.URL = &newURL
	return r.orig.RoundTrip(newReq)
}

func TestUploadFileToDrive_Success(t *testing.T) {
	// create temp file to upload
	tmpFile, err := os.CreateTemp(t.TempDir(), "upload-*.txt")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	content := []byte("hello drive")
	if _, err := tmpFile.Write(content); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	_ = tmpFile.Close()

	// test server to emulate Drive upload endpoint
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// ensure auth header present
		if got := req.Header.Get("Authorization"); got != "Bearer tok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		// parse content-type and boundary
		ct := req.Header.Get("Content-Type")
		mediatype, params, err := mime.ParseMediaType(ct)
		if err != nil || !strings.HasPrefix(mediatype, "multipart/") {
			http.Error(w, "bad content-type", http.StatusBadRequest)
			return
		}
		boundary, ok := params["boundary"]
		if !ok {
			http.Error(w, "missing boundary", http.StatusBadRequest)
			return
		}
		mr := multipart.NewReader(req.Body, boundary)

		// first part: metadata JSON
		metaPart, err := mr.NextPart()
		if err != nil {
			http.Error(w, "missing meta part", http.StatusBadRequest)
			return
		}
		metaBytes, _ := io.ReadAll(metaPart)
		var meta map[string]interface{}
		if err := json.Unmarshal(metaBytes, &meta); err != nil {
			http.Error(w, "bad meta json", http.StatusBadRequest)
			return
		}
		// check parents includes folder id
		parents, _ := meta["parents"].([]interface{})
		if len(parents) == 0 || parents[0].(string) != "folder123" {
			http.Error(w, "bad parents", http.StatusBadRequest)
			return
		}

		// second part: file
		filePart, err := mr.NextPart()
		if err != nil {
			http.Error(w, "missing file part", http.StatusBadRequest)
			return
		}
		fileBytes, _ := io.ReadAll(filePart)
		if !bytes.Equal(fileBytes, content) {
			http.Error(w, "file mismatch", http.StatusBadRequest)
			return
		}

		// respond with JSON id
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"uploaded-file-1"}`))
	}))
	defer ts.Close()

	// rewrite http.DefaultTransport for the duration of the test
	orig := http.DefaultTransport
	http.DefaultTransport = &rewritingRoundTripper{orig: orig, targetBase: mustParseURL(ts.URL)}
	t.Cleanup(func() { http.DefaultTransport = orig })

	// call UploadFileToDrive
	id, err := UploadFileToDrive("tok", "folder123", tmpFile.Name())
	if err != nil {
		t.Fatalf("UploadFileToDrive error: %v", err)
	}
	if id != "uploaded-file-1" {
		t.Fatalf("unexpected id: %q", id)
	}
}

func TestUploadFileToDrive_MissingParams(t *testing.T) {
	// missing accessToken
	if _, err := UploadFileToDrive("", "f", "p"); err == nil {
		t.Fatal("expected error for empty accessToken")
	}
	// missing folderID
	if _, err := UploadFileToDrive("tok", "", "p"); err == nil {
		t.Fatal("expected error for empty folderID")
	}
	// missing filePath
	if _, err := UploadFileToDrive("tok", "f", ""); err == nil {
		t.Fatal("expected error for empty filePath")
	}
}

func TestUploadFileToDrive_FileIsDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := UploadFileToDrive("tok", "f", dir); err == nil {
		t.Fatal("expected error when filePath is a directory")
	}
}

func TestUploadFileToDrive_Non2xxResponse(t *testing.T) {
	// create temp file
	tmpFile, err := os.CreateTemp(t.TempDir(), "upload-*.txt")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	_ = tmpFile.Close()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	orig := http.DefaultTransport
	http.DefaultTransport = &rewritingRoundTripper{orig: orig, targetBase: mustParseURL(ts.URL)}
	t.Cleanup(func() { http.DefaultTransport = orig })

	if _, err := UploadFileToDrive("tok", "folder", tmpFile.Name()); err == nil {
		t.Fatal("expected error for non-2xx response")
	}
}

func TestUploadFileToDrive_InvalidJSONResponse(t *testing.T) {
	// create temp file
	tmpFile, err := os.CreateTemp(t.TempDir(), "upload-*.txt")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	_ = tmpFile.Close()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer ts.Close()

	orig := http.DefaultTransport
	http.DefaultTransport = &rewritingRoundTripper{orig: orig, targetBase: mustParseURL(ts.URL)}
	t.Cleanup(func() { http.DefaultTransport = orig })

	if _, err := UploadFileToDrive("tok", "folder", tmpFile.Name()); err == nil {
		t.Fatal("expected error for invalid json response")
	}
}

// mustParseURL is a small test helper.
func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
