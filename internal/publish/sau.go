package publish

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SAUUploadInput configures a single invocation of the social-auto-upload (sau) sidecar CLI.
// Used for TikTok (and optional multi-platform) posting STRICTLY AFTER human QC approval.
type SAUUploadInput struct {
	BinaryPath  string
	Platform    string // "tiktok", "youtube", "instagram"
	AccountName string // Owner-managed session profile name in sau
	FilePath    string // Approved local MP4 file
	Title       string
	Caption     string
	Tags        []string
	ScheduledAt *time.Time
}

// ResolveSAUBinary finds the configured or PATH-installed `sau` sidecar CLI.
func ResolveSAUBinary(configured string) string {
	if c := strings.TrimSpace(configured); c != "" {
		if _, err := os.Stat(c); err == nil {
			return c
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	for _, cand := range []string{"sau", "/data/workspace/klipforge/.venv/bin/sau"} {
		if p, err := exec.LookPath(cand); err == nil {
			return p
		}
	}
	return ""
}

// RunSAUSidecar shells out to dreammis/social-auto-upload (`sau`) with a generated metadata manifest
// and the approved MP4 file. Never calls the audit-blocked official TikTok Content Posting API.
func RunSAUSidecar(ctx context.Context, in SAUUploadInput) (string, error) {
	bin := ResolveSAUBinary(in.BinaryPath)
	if bin == "" {
		return "", fmt.Errorf("sidecar social-auto-upload ('sau') tidak ditemukan di PATH atau Settings.sauPath — install dreammis/social-auto-upload & login sesi akun terlebih dahulu")
	}
	if _, err := os.Stat(in.FilePath); err != nil {
		return "", fmt.Errorf("file MP4 untuk publish tidak ditemukan: %s", in.FilePath)
	}
	platform := strings.ToLower(strings.TrimSpace(in.Platform))
	if platform == "" {
		platform = "tiktok"
	}

	// Write a sidecar payload manifest beside temp dir for traceability
	tmpDir, err := os.MkdirTemp("", "kf-sau-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmpDir)
	metaPath := filepath.Join(tmpDir, "publish_kit.json")
	if b, err := json.MarshalIndent(in, "", "  "); err == nil {
		_ = os.WriteFile(metaPath, b, 0o600)
	}

	args := []string{"upload", "--platform", platform, "--file", in.FilePath}
	if acc := strings.TrimSpace(in.AccountName); acc != "" {
		args = append(args, "--account", acc)
	}
	if title := strings.TrimSpace(in.Title); title != "" {
		args = append(args, "--title", title)
	}
	if capTxt := strings.TrimSpace(in.Caption); capTxt != "" {
		args = append(args, "--desc", capTxt)
	}
	if len(in.Tags) > 0 {
		capLow := strings.ToLower(in.Caption)
		var missingTags []string
		for _, t := range in.Tags {
			clean := strings.TrimPrefix(strings.TrimSpace(t), "#")
			if clean == "" {
				continue
			}
			if !strings.Contains(capLow, "#"+strings.ToLower(clean)) {
				missingTags = append(missingTags, clean)
			}
		}
		if len(missingTags) > 0 {
			args = append(args, "--tags", strings.Join(missingTags, ","))
		}
	}
	if in.ScheduledAt != nil && in.ScheduledAt.After(time.Now()) {
		args = append(args, "--schedule", in.ScheduledAt.Format("2006-01-02 15:04"))
	}

	cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return "", fmt.Errorf("sau sidecar gagal (%v): %s", err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
