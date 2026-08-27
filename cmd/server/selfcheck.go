package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"caption-release-workbench/internal/release"
	"caption-release-workbench/internal/repository"
	"caption-release-workbench/internal/validator"
	"caption-release-workbench/internal/web"
	"caption-release-workbench/internal/workflow"
)

type selfcheckClient struct {
	baseURL string
	client  *http.Client
	version int64
	project string
	token   string
}

func runSelfcheck(address string) error {
	host, _, _ := net.SplitHostPort(address)
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("selfcheck 只允许使用回环监听地址")
	}
	temporary, err := os.MkdirTemp("", "caption-workbench-selfcheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	store, err := repository.Open(filepath.Join(temporary, "selfcheck.db"))
	if err != nil {
		return err
	}
	defer store.Close()
	credentials, err := release.Generate("selfcheck-ed25519-v1")
	if err != nil {
		return err
	}
	application := workflow.New(store, validator.New(), credentials)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("selfcheck 监听 %s: %w", address, err)
	}
	server := &http.Server{Handler: web.New(application), ReadHeaderTimeout: 2 * time.Second}
	done := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		done <- err
	}()
	contextWithTimeout, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := &selfcheckClient{baseURL: "http://" + listener.Addr().String(), client: &http.Client{Timeout: 3 * time.Second}}
	flowError := client.completeFlow(contextWithTimeout)
	shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
	shutdownError := server.Shutdown(shutdownContext)
	shutdownCancel()
	serveError := <-done
	if flowError != nil {
		return fmt.Errorf("selfcheck 业务流程失败: %w", flowError)
	}
	if shutdownError != nil {
		return fmt.Errorf("selfcheck 关闭失败: %w", shutdownError)
	}
	if serveError != nil {
		return fmt.Errorf("selfcheck HTTP 服务失败: %w", serveError)
	}
	fmt.Printf("selfcheck 通过：项目 %s 已完成建档、导入、复核、冻结、签发、验证与发布\n", client.project)
	return nil
}

func (c *selfcheckClient) completeFlow(ctx context.Context) error {
	var created workflow.CommandResponse
	if err := c.json(ctx, http.MethodPost, "/api/projects", map[string]any{
		"idempotencyKey": "selfcheck-create", "title": "自检演出", "performanceVersion": "巡演版",
		"language": "zh-CN", "frameRate": 25, "durationMs": 60000,
		"producerId": "selfcheck-producer", "reviewerId": "selfcheck-reviewer",
	}, &created); err != nil {
		return err
	}
	c.project, c.version = created.ProjectID, created.Version
	cleanVTT := "WEBVTT\n\n00:00:01.000 --> 00:00:03.000\n<v 林岚>灯亮了。\n\n00:00:03.200 --> 00:00:05.600\n<v 周远>我们该出发了。\n\n00:00:06.000 --> 00:00:08.000\n[远处传来钟声]\n"
	if err := c.command(ctx, "/api/projects/"+c.project+"/revisions", map[string]any{"actorId": "selfcheck-producer", "webvtt": cleanVTT, "changeNote": "完整流程自检"}); err != nil {
		return err
	}
	var snapshot struct {
		Findings []domainFinding `json:"findings"`
	}
	if err := c.json(ctx, http.MethodGet, "/api/projects/"+c.project, nil, &snapshot); err != nil {
		return err
	}
	if len(snapshot.Findings) != 0 {
		return fmt.Errorf("预期清洁时间轴无问题，实际为 %d", len(snapshot.Findings))
	}
	if err := c.command(ctx, "/api/projects/"+c.project+"/submit-review", map[string]any{"actorId": "selfcheck-producer"}); err != nil {
		return err
	}
	if err := c.command(ctx, "/api/projects/"+c.project+"/reviews", map[string]any{"actorId": "selfcheck-reviewer", "decision": "approve", "comment": "自检复核通过"}); err != nil {
		return err
	}
	if err := c.command(ctx, "/api/projects/"+c.project+"/freeze", map[string]any{"actorId": "selfcheck-publisher"}); err != nil {
		return err
	}
	var issued workflow.CommandResponse
	if err := c.commandInto(ctx, "/api/projects/"+c.project+"/credentials", map[string]any{"actorId": "selfcheck-publisher"}, &issued); err != nil {
		return err
	}
	c.token = issued.Token
	var verification struct {
		Valid bool   `json:"valid"`
		Code  string `json:"code"`
	}
	if err := c.json(ctx, http.MethodPost, "/api/credentials/verify", map[string]any{"token": c.token, "projectId": c.project}, &verification); err != nil {
		return err
	}
	if !verification.Valid || verification.Code != "valid" {
		return errors.New("签发凭据未通过离线验证")
	}
	if err := c.command(ctx, "/api/projects/"+c.project+"/publish", map[string]any{"actorId": "selfcheck-publisher"}); err != nil {
		return err
	}
	return nil
}

type domainFinding struct {
	ID string `json:"findingId"`
}

func (c *selfcheckClient) command(ctx context.Context, path string, fields map[string]any) error {
	var response workflow.CommandResponse
	return c.commandInto(ctx, path, fields, &response)
}

func (c *selfcheckClient) commandInto(ctx context.Context, path string, fields map[string]any, response *workflow.CommandResponse) error {
	fields["expectedVersion"] = c.version
	fields["idempotencyKey"] = "selfcheck-" + strconv.FormatInt(c.version, 10) + "-" + strings.ReplaceAll(path, "/", "-")
	if err := c.json(ctx, http.MethodPost, path, fields, response); err != nil {
		return err
	}
	c.version = response.Version
	return nil
}

func (c *selfcheckClient) json(ctx context.Context, method, path string, body any, destination any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s %s 返回 %d：%s", method, path, response.StatusCode, strings.TrimSpace(string(data)))
	}
	if destination != nil {
		if err := json.Unmarshal(data, destination); err != nil {
			return fmt.Errorf("解析 %s 响应: %w", path, err)
		}
	}
	return nil
}
