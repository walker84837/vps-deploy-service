package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
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
	artifactFile := fmt.Sprintf("/tmp/%s.tar.gz", payload.Project)
	if err := downloadArtifact(payload.ArtifactID, payload.GitHubToken, artifactFile); err != nil {
		http.Error(w, "failed to download artifact: "+err.Error(), http.StatusInternalServerError)
		return
	}

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

	// Step 5: extract tar.gz
	if err := extractTarGz(artifactFile, dest); err != nil {
		http.Error(w, "failed to extract artifact: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("Deployment complete: %s\n", dest)
	w.Write([]byte("success"))
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
func downloadArtifact(artifactID, token, dest string) error {
	// GitHub API: GET /repos/:owner/:repo/actions/artifacts/:artifact_id/zip
	// Use token in Authorization header
	url := fmt.Sprintf("https://api.github.com/repos/<OWNER>/<REPO>/actions/artifacts/%s/zip", artifactID)
	cmd := exec.Command("curl", "-L", "-H", "Authorization: token "+token, "-o", dest, url)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func verifySignature(filePath, sigData, pubKey string) error {
	// read the artifact
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}

	// parse the public key
	publicKey, err := minisign.NewPublicKey(pubKey)
	if err != nil {
		return fmt.Errorf("invalid public key: %w", err)
	}

	// parse the signature
	sig, err := minisign.DecodeSignature(sigData)
	if err != nil {
		return fmt.Errorf("invalid signature: %w", err)
	}

	// verify
	valid, err := publicKey.Verify(data, sig)
	if err != nil {
		return fmt.Errorf("signature verification error: %w", err)
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
