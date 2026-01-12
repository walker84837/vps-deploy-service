package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jedisct1/go-minisign"
)

// AreaMap defines alias -> base path
type AreaMap map[string]string

// WebhookPayload represents incoming JSON
type WebhookPayload struct {
	Area        string `json:"area"`
	Project     string `json:"project"`
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	ArtifactID  string `json:"artifact_id"`
	GitHubToken string `json:"github_token"`
	Signature   string `json:"signature"` // minisign signature
}

var areas AreaMap

func main() {
	// Load area map from file
	f, err := os.Open("areas.json")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&areas); err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/deploy", deployHandler)
	log.Println("Listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func deployHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	var payload WebhookPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	dest, err := computeFinalPath(payload.Area, payload.Project)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("Deploying project '%s' to '%s'\n", payload.Project, dest)

	// Step 1: download artifact
	artifactFile, err := downloadArtifact(payload.Owner, payload.Repo, payload.ArtifactID, payload.GitHubToken, payload.Project)
	if err != nil {
		http.Error(w, "failed to download artifact: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(artifactFile) // clean up temp file

	// Step 2: verify signature
	pubKeyBytes, err := os.ReadFile("minisign.pub")
	if err != nil {
		http.Error(w, "missing public key: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := verifySignature(artifactFile, payload.Signature, string(pubKeyBytes)); err != nil {
		http.Error(w, "signature verification failed: "+err.Error(), http.StatusForbidden)
		return
	}

	// Step 3: remove old folder
	if err := os.RemoveAll(dest); err != nil {
		http.Error(w, "failed to remove old project folder: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Step 4: recreate folder
	if err := os.MkdirAll(dest, 0755); err != nil {
		http.Error(w, "failed to create project folder: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Step 5: extract tar.gz from inside the zip
	if err := extractTarGzFromZip(artifactFile, dest); err != nil {
		http.Error(w, "failed to extract artifact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("Deployment complete: %s\n", dest)
	w.Write([]byte("success"))
}

// extractTarGzFromZip extracts the first .tar.gz file from a ZIP to destPath
func extractTarGzFromZip(zipPath, destPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if strings.HasSuffix(f.Name, ".tar.gz") {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()

			outFile, err := os.Create(destPath)
			if err != nil {
				return err
			}
			defer outFile.Close()

			if _, err := io.Copy(outFile, rc); err != nil {
				return err
			}

			return nil // successfully extracted
		}
	}

	return errors.New(".tar.gz not found in ZIP")
}

// computeFinalPath combines area alias and project name safely
func computeFinalPath(area, project string) (string, error) {
	base, ok := areas[area]
	if !ok {
		return "", errors.New("unknown area alias: " + area)
	}
	dest := filepath.Join(base, project)
	// prevent escaping the base folder
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return "", err
	}
	absBase, _ := filepath.Abs(base)
	if !strings.HasPrefix(absDest, absBase) {
		return "", errors.New("project path escapes area base")
	}
	return absDest, nil
}

// downloadArtifact downloads a GitHub workflow artifact using a token
// downloadArtifact downloads a GitHub workflow artifact using a token
func downloadArtifact(owner, repo, artifactID, token, project string) (string, error) {
	if token == "" {
		return "", errors.New("missing GitHub token")
	}
	if owner == "" || repo == "" {
		return "", errors.New("missing owner or repo")
	}

	// Use archive_format=zip
	url := fmt.Sprintf(
		"https://api.github.com/repos/%s/%s/actions/artifacts/%s/zip",
		owner, repo, artifactID,
	)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// Allow following redirects (the 302)
			return nil
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusFound {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to download artifact: %s\n%s", resp.Status, string(bodyBytes))
	}

	// If 302 Found, the redirect location is the actual zip URL
	if resp.StatusCode == http.StatusFound {
		redirectURL := resp.Header.Get("Location")
		if redirectURL == "" {
			return "", errors.New("artifact redirect location missing")
		}
		// Download from redirect URL
		resp, err = http.Get(redirectURL)
		if err != nil {
			return "", fmt.Errorf("failed to download redirected artifact: %w", err)
		}
		defer resp.Body.Close()
	}

	// Create a temp file path using os.TempDir
	dest := filepath.Join(os.TempDir(), fmt.Sprintf("%s.zip", project))
	outFile, err := os.Create(dest)
	if err != nil {
		return "", fmt.Errorf("failed to create file: %w", err)
	}
	defer outFile.Close()

	if _, err := io.Copy(outFile, resp.Body); err != nil {
		return "", fmt.Errorf("failed to write file: %w", err)
	}

	return dest, nil
}

// verifySignature reads the file, decodes the base64 signature, and verifies it
func verifySignature(filePath, base64Sig, pubKey string) error {
	// Read the file to verify
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	// Decode the base64-encoded signature
	sigBytes, err := base64.StdEncoding.DecodeString(base64Sig)
	if err != nil {
		return errors.New("invalid base64 signature: " + err.Error())
	}

	// Parse the minisign public key
	publicKey, err := minisign.NewPublicKey(pubKey)
	if err != nil {
		return errors.New("invalid public key: " + err.Error())
	}

	// Decode the minisign signature
	sig, err := minisign.DecodeSignature(string(sigBytes))
	if err != nil {
		return errors.New("invalid signature format: " + err.Error())
	}

	// Verify the signature
	valid, err := publicKey.Verify(data, sig)
	if err != nil {
		return errors.New("signature verification error: " + err.Error())
	}
	if !valid {
		return errors.New("signature verification failed")
	}

	return nil
}

// extractTarGz extracts a tar.gz to dest
func extractTarGz(file, dest string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			outFile, err := os.Create(target)
			if err != nil {
				return err
			}
			if _, err := io.Copy(outFile, tr); err != nil {
				outFile.Close()
				return err
			}
			outFile.Close()
		default:
			// skip other types
		}
	}
	return nil
}
