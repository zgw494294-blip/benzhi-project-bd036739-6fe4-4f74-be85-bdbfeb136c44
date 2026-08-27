package domain

import (
	"errors"
	"strings"
	"time"
)

type Project struct {
	ID                 string    `json:"projectId"`
	Title              string    `json:"title"`
	PerformanceVersion string    `json:"performanceVersion"`
	Language           string    `json:"language"`
	FrameRate          float64   `json:"frameRate"`
	DurationMS         int64     `json:"durationMs"`
	ProducerID         string    `json:"producerId"`
	ReviewerID         string    `json:"reviewerId"`
	Status             Status    `json:"status"`
	Version            int64     `json:"version"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

func (p Project) Validate() error {
	if strings.TrimSpace(p.Title) == "" || len(p.Title) > 160 {
		return errors.New("演出标题不能为空且不能超过 160 字符")
	}
	if strings.TrimSpace(p.PerformanceVersion) == "" || len(p.PerformanceVersion) > 80 {
		return errors.New("演出版本不能为空且不能超过 80 字符")
	}
	if strings.TrimSpace(p.Language) == "" || len(p.Language) > 32 {
		return errors.New("语言不能为空且不能超过 32 字符")
	}
	if p.FrameRate <= 0 || p.FrameRate > 240 {
		return errors.New("帧率必须在 0 到 240 之间")
	}
	if p.DurationMS <= 0 || p.DurationMS > 24*60*60*1000 {
		return errors.New("时长必须大于零且不超过 24 小时")
	}
	if strings.TrimSpace(p.ProducerID) == "" || strings.TrimSpace(p.ReviewerID) == "" {
		return errors.New("制作员和校审员均不能为空")
	}
	if p.ProducerID == p.ReviewerID {
		return errors.New("制作员与校审员必须是不同人员")
	}
	return nil
}

type ProjectListItem struct {
	Project          Project `json:"project"`
	LatestRevisionNo int     `json:"latestRevisionNo"`
	OpenFindings     int     `json:"openFindings"`
	OpenBlockers     int     `json:"openBlockers"`
}
