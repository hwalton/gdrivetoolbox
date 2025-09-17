package deploy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
)

func DeployPDF(accessToken string, fileName string, versionSafe string, tempFolderID string, folderID string, oldFolderID string, sopDir string) error {
	// Sanity checks
	if fileName == "" || accessToken == "" || tempFolderID == "" || folderID == "" {
		return errors.New("missing required variable(s): fileName, accessToken, tempFolderID, folderID")
	}

	pdfFile := fileName + ".pdf"

	pdfPath := filepath.Join(sopDir, pdfFile)
	if _, err := os.Stat(pdfPath); err != nil {
		return fmt.Errorf("PDF '%s' not found", pdfPath)
	}
	if versionSafe == "" {
		return errors.New("version-safe.txt missing or empty, or VERSION_SUFFIX not set")
	}

	// Query for existing file
	encodedName := url.QueryEscape(pdfFile)
	queryURL := fmt.Sprintf(
		"https://www.googleapis.com/drive/v3/files?q='%s'+in+parents+and+name='%s'+and+trashed=false&fields=files(id,name,description)",
		folderID, encodedName,
	)
	req, _ := http.NewRequest("GET", queryURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var result struct {
		Files []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}
	var existingFileID, existingFileDesc string
	if len(result.Files) > 0 {
		existingFileID = result.Files[0].ID
		existingFileDesc = result.Files[0].Description
	}

	if existingFileID != "" && existingFileDesc == versionSafe {
		fmt.Println("-- Skipped: Version already deployed")
		return nil
	}

	// Archive old version if needed
	if existingFileID != "" && oldFolderID != "" {
		renamedFile := fileName + "-" + (existingFileDesc)
		if existingFileDesc == "" || existingFileDesc == "null" {
			renamedFile = fileName + "-unknown"
		}
		renamedFile += ".pdf"

		// Rename
		renameURL := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s", existingFileID)
		renameBody := fmt.Sprintf(`{"name": "%s"}`, renamedFile)
		req, _ := http.NewRequest("PATCH", renameURL, bytes.NewBuffer([]byte(renameBody)))
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to rename existing file: %w", err)
		}
		resp.Body.Close()

		// Move
		moveURL := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?addParents=%s&removeParents=%s&fields=id,parents", existingFileID, oldFolderID, folderID)
		req, _ = http.NewRequest("PATCH", moveURL, nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to move old file to archive: %w", err)
		}
		resp.Body.Close()
		fmt.Printf("Archived old version as '%s'\n", renamedFile)
	} else if existingFileID != "" {
		fmt.Println("Warning: oldFolderID not set; existing file will be deleted")
		delURL := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s", existingFileID)
		req, _ := http.NewRequest("DELETE", delURL, nil)
		req.Header.Set("Authorization", "Bearer "+accessToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("failed to delete existing file: %w", err)
		}
		defer resp.Body.Close()
		// Expect 204 No Content on success; some endpoints may return 200
		if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("failed to delete existing file: status %d: %s", resp.StatusCode, string(body))
		}
	} else {
		fmt.Println("No existing version found")
	}

	// Upload new file (multipart/related)
	metadata := map[string]interface{}{
		"name":        pdfFile,
		"parents":     []string{tempFolderID},
		"description": versionSafe,
	}
	metadataJSON, _ := json.Marshal(metadata)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// Add metadata part
	metaPart, _ := writer.CreatePart(map[string][]string{
		"Content-Type": {"application/json; charset=UTF-8"},
	})
	metaPart.Write(metadataJSON)

	// Add PDF part
	pdfPart, _ := writer.CreatePart(map[string][]string{
		"Content-Type": {"application/pdf"},
	})

	osPDFFile, err := os.Open(pdfPath)
	if err != nil {
		return err
	}
	defer osPDFFile.Close()
	io.Copy(pdfPart, osPDFFile)
	writer.Close()

	uploadURL := "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart"
	req, _ = http.NewRequest("POST", uploadURL, &buf)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()
	uploadRespBody, _ := io.ReadAll(resp.Body)
	var uploadResult struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(uploadRespBody, &uploadResult); err != nil || uploadResult.ID == "" {
		return fmt.Errorf("upload failed: %s", string(uploadRespBody))
	}
	newFileID := uploadResult.ID
	fmt.Printf("Uploaded new file: ID %s\n", newFileID)

	// Set sharing restrictions
	permURL := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s", newFileID)
	permBody := []byte(`{"copyRequiresWriterPermission": true, "writersCanShare": false}`)
	req, _ = http.NewRequest("PATCH", permURL, bytes.NewBuffer(permBody))
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	http.DefaultClient.Do(req) // ignore errors

	// Move to final folder
	moveNewURL := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s?addParents=%s&removeParents=%s&fields=id,parents", newFileID, folderID, tempFolderID)
	req, _ = http.NewRequest("PATCH", moveNewURL, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("move to final folder failed: %w", err)
	}
	defer resp.Body.Close()
	moveRespBody, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(moveRespBody, []byte(`"id"`)) {
		return fmt.Errorf("upload succeeded, but move failed: %s", string(moveRespBody))
	}
	fmt.Println("Deployment successful: moved to final folder.")
	return nil
}

func CheckRemoteVersionExists(accessToken string, fileName string, folderID string, versionSafe string) (bool, error) {
	fmt.Println("  accessToken:", accessToken != "")
	fmt.Println("  fileName:", fileName)
	fmt.Println("  folderID:", folderID)
	fmt.Println("  versionSafe:", versionSafe)

	if accessToken == "" {
		return false, fmt.Errorf("ACCESS_TOKEN is not set")
	}
	if fileName == "" || folderID == "" || versionSafe == "" {
		return false, fmt.Errorf("missing required variable(s): FileName, FolderID, VersionSafe")
	}

	pdfFile := fileName + ".pdf"

	encodedName := url.QueryEscape(pdfFile)
	url := fmt.Sprintf(
		"https://www.googleapis.com/drive/v3/files?q='%s'+in+parents+and+name='%s'+and+trashed=false&fields=files(id,name,description)",
		folderID, encodedName,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var result struct {
		Files []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false, err
	}

	if len(result.Files) > 0 && result.Files[0].Description == versionSafe {
		fmt.Printf("-- Skipped: Exact version already deployed (%s)\n", pdfFile)
		return true, nil
	}
	fmt.Printf("-- Will deploy: New or unmatched version for %s\n", pdfFile)
	return false, nil
}

func UploadFileToDrive(accessToken, folderID, filePath string) (string, error) {
	if accessToken == "" {
		return "", errors.New("accessToken is required")
	}
	if folderID == "" {
		return "", errors.New("folderID is required")
	}
	if filePath == "" {
		return "", errors.New("filePath is required")
	}

	finfo, err := os.Stat(filePath)
	if err != nil {
		return "", fmt.Errorf("stat file: %w", err)
	}
	if finfo.IsDir() {
		return "", fmt.Errorf("filePath is a directory")
	}

	fileName := filepath.Base(filePath)

	// metadata JSON
	meta := map[string]interface{}{
		"name":    fileName,
		"parents": []string{folderID},
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}

	// prepare multipart/related body
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	// metadata part
	metaHeader := make(textproto.MIMEHeader)
	metaHeader.Set("Content-Type", "application/json; charset=UTF-8")
	metaPart, err := writer.CreatePart(metaHeader)
	if err != nil {
		return "", fmt.Errorf("create metadata part: %w", err)
	}
	if _, err := metaPart.Write(metaJSON); err != nil {
		return "", fmt.Errorf("write metadata part: %w", err)
	}

	// file part
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	ctype := mime.TypeByExtension(filepath.Ext(fileName))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	fileHeader := make(textproto.MIMEHeader)
	fileHeader.Set("Content-Type", ctype)
	// filename in disposition (Drive doesn't require form-data disposition, but keep for clarity)
	fileHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, fileName))
	filePart, err := writer.CreatePart(fileHeader)
	if err != nil {
		return "", fmt.Errorf("create file part: %w", err)
	}
	if _, err := io.Copy(filePart, f); err != nil {
		return "", fmt.Errorf("copy file part: %w", err)
	}

	// finish writer
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart writer: %w", err)
	}

	// POST to Drive upload endpoint using multipart/related
	uploadURL := "https://www.googleapis.com/upload/drive/v3/files?uploadType=multipart"
	req, err := http.NewRequest("POST", uploadURL, &buf)
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	// Use multipart/related with the writer boundary
	req.Header.Set("Content-Type", "multipart/related; boundary="+writer.Boundary())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("upload failed: status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("decode upload response: %w", err)
	}
	if result.ID == "" {
		return "", fmt.Errorf("upload succeeded but returned empty id: %s", string(body))
	}
	return result.ID, nil
}
