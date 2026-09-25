package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/codex2api/proxy"
)

const managedUpdateReason = "当前为星桥集成镜像，请通过已审核的源码发布流程更新，以保留智能运维和数据库兼容性"

var sourceCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

type systemSourceHead struct {
	SHA         string
	PublishedAt string
}

func (u *systemUpdater) managedInfo() *systemUpdateInfo {
	return &systemUpdateInfo{CurrentVersion: u.currentVersion, RuntimeOS: u.goos, RuntimeArch: u.goarch, Mode: "source_image", Supported: false, UnsupportedReason: managedUpdateReason, SourceRepository: systemUpdateRepo, SourceRevision: u.sourceRevision, SourceTree: u.sourceTree, UpstreamRevision: u.upstreamRevision, CheckStatus: "unknown"}
}

func (u *systemUpdater) inspectManaged(ctx context.Context) (*systemUpdateInspection, error) {
	info := u.managedInfo()
	if !sourceCommitPattern.MatchString(u.upstreamRevision) {
		return nil, fmt.Errorf("集成镜像缺少有效的上游 commit 来源")
	}
	u.releaseCacheMu.Lock()
	defer u.releaseCacheMu.Unlock()
	if u.sourceCache == nil || time.Now().After(u.sourceCacheExpiresAt) {
		if u.fetchSourceHead == nil {
			return nil, fmt.Errorf("未配置源码更新检查")
		}
		head, err := u.fetchSourceHead(ctx)
		if err != nil {
			return nil, err
		}
		if head == nil || !sourceCommitPattern.MatchString(head.SHA) {
			return nil, fmt.Errorf("上游返回了无效的 commit")
		}
		u.sourceCache = &systemSourceHead{SHA: head.SHA, PublishedAt: head.PublishedAt}
		u.sourceCacheExpiresAt = time.Now().Add(systemUpdateReleaseCacheTTL)
	}
	head := u.sourceCache
	info.CheckStatus = "checked"
	info.LatestRevision = head.SHA
	info.LatestVersion = head.SHA[:12]
	info.ReleaseURL = "https://github.com/" + systemUpdateRepo + "/commit/" + head.SHA
	info.PublishedAt = head.PublishedAt
	info.HasUpdate = head.SHA != u.upstreamRevision
	if info.HasUpdate {
		info.Warning = "上游 main 有不同提交，需合并审查并构建新的集成镜像"
	}
	return &systemUpdateInspection{info: info}, nil
}

func (c *defaultSystemReleaseClient) FetchSourceHead(ctx context.Context) (*systemSourceHead, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+systemUpdateRepo+"/commits/main", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", systemUpdateUserAgent)
	proxy.ApplyGithubAuth(req)
	resp, err := c.apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub source API 返回 HTTP %d", resp.StatusCode)
	}
	var data struct {
		SHA    string `json:"sha"`
		Commit struct {
			Committer struct {
				Date string `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&data); err != nil {
		return nil, err
	}
	return &systemSourceHead{SHA: data.SHA, PublishedAt: data.Commit.Committer.Date}, nil
}
