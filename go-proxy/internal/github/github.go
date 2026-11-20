package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var validRepoName = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,100}$`)

type StatusInfo struct {
	IsRepo     bool   `json:"isRepo"`
	Branch     string `json:"branch"`
	OriginURL  string `json:"originUrl"`
	DirtyCount int    `json:"dirtyCount"`
	GHUser     string `json:"ghUser"`
	Error      string `json:"error,omitempty"`
}

func runCmd(ctx context.Context, dir, exe string, args ...string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, exe, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), err
}

func Status(ctx context.Context, workspace, ghExe string) StatusInfo {
	if ghExe == "" {
		ghExe = "gh"
	}
	info := StatusInfo{}
	if workspace == "" {
		info.Error = "PI_WORKSPACE is not set"
		return info
	}

	if out, _, err := runCmd(ctx, workspace, "git", "rev-parse", "--is-inside-work-tree"); err == nil && out == "true" {
		info.IsRepo = true
		if br, _, err := runCmd(ctx, workspace, "git", "branch", "--show-current"); err == nil {
			info.Branch = br
		}
		if orig, _, err := runCmd(ctx, workspace, "git", "remote", "get-url", "origin"); err == nil {
			info.OriginURL = orig
		}
		if st, _, err := runCmd(ctx, workspace, "git", "status", "--porcelain"); err == nil && st != "" {
			info.DirtyCount = len(strings.Split(st, "\n"))
		}
	}

	out, errOut, _ := runCmd(ctx, "", ghExe, "auth", "status")
	combined := out + "\n" + errOut
	for _, line := range strings.Split(combined, "\n") {
		if idx := strings.Index(line, "account "); idx >= 0 {
			rest := strings.TrimSpace(line[idx+len("account "):])
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				info.GHUser = fields[0]
				break
			}
		}
	}
	return info
}

func CreateRepo(ctx context.Context, workspace, ghExe, name, visibility, description string) (string, error) {
	if workspace == "" {
		return "", errors.New("PI_WORKSPACE is not set")
	}
	if ghExe == "" {
		ghExe = "gh"
	}
	name = strings.TrimSpace(name)
	if !validRepoName.MatchString(name) {
		return "", errors.New("invalid repository name (use [a-zA-Z0-9._-])")
	}
	visFlag := "--private"
	if strings.ToLower(strings.TrimSpace(visibility)) == "public" {
		visFlag = "--public"
	}

	if out, _, err := runCmd(ctx, workspace, "git", "rev-parse", "--is-inside-work-tree"); err != nil || out != "true" {
		if _, errOut, err := runCmd(ctx, workspace, "git", "init"); err != nil {
			return "", fmt.Errorf("git init: %s (%v)", errOut, err)
		}
	}
	// Ensure at least one commit exists before --push
	if _, _, err := runCmd(ctx, workspace, "git", "rev-parse", "HEAD"); err != nil {
		_, _, _ = runCmd(ctx, workspace, "git", "add", "-A")
		if _, errOut, err := runCmd(ctx, workspace, "git", "commit", "-m", "Initial commit"); err != nil {
			return "", fmt.Errorf("git commit: %s (%v)", errOut, err)
		}
	}

	args := []string{"repo", "create", name, visFlag, "--source=.", "--remote=origin", "--push"}
	if strings.TrimSpace(description) != "" {
		args = append(args, "--description", strings.TrimSpace(description))
	}
	out, errOut, err := runCmd(ctx, workspace, ghExe, args...)
	if err != nil {
		return "", fmt.Errorf("gh repo create: %s %s (%v)", out, errOut, err)
	}
	if out != "" {
		return out, nil
	}
	return errOut, nil
}

func Push(ctx context.Context, workspace string) (string, error) {
	if workspace == "" {
		return "", errors.New("PI_WORKSPACE is not set")
	}
	out, errOut, err := runCmd(ctx, workspace, "git", "push", "-u", "origin", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git push: %s %s (%v)", out, errOut, err)
	}
	if out != "" {
		return out, nil
	}
	return errOut, nil
}

func CreatePR(ctx context.Context, workspace, ghExe, title, body, base string) (string, error) {
	if workspace == "" {
		return "", errors.New("PI_WORKSPACE is not set")
	}
	if ghExe == "" {
		ghExe = "gh"
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("PR title is required")
	}
	args := []string{"pr", "create", "--title", title, "--body", body}
	if strings.TrimSpace(base) != "" {
		args = append(args, "--base", strings.TrimSpace(base))
	}
	out, errOut, err := runCmd(ctx, workspace, ghExe, args...)
	if err != nil {
		return "", fmt.Errorf("gh pr create: %s %s (%v)", out, errOut, err)
	}
	if out != "" {
		return out, nil
	}
	return errOut, nil
}
