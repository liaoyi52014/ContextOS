package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/contextos/contextos/internal/types"
	"gopkg.in/yaml.v3"
)

type skillImportRequest struct {
	types.SkillDocument
	TempFileID string `json:"temp_file_id"`
	Wait       *bool  `json:"wait,omitempty"`
}

func resolveSkillDocument(req skillImportRequest) (types.SkillDocument, error) {
	if req.TempFileID == "" {
		return req.SkillDocument, nil
	}

	meta, err := loadTempUpload(req.TempFileID)
	if err != nil {
		return types.SkillDocument{}, err
	}
	if meta == nil {
		return types.SkillDocument{}, &types.AppError{Code: types.ErrNotFound, Message: fmt.Sprintf("temp file %s not found", req.TempFileID)}
	}
	return loadSkillDocumentFromPath(meta.Path)
}

func loadSkillDocumentFromPath(path string) (types.SkillDocument, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".zip":
		return loadSkillDocumentFromZip(path)
	case ".yaml", ".yml":
		return loadSkillDocumentFromYAML(path)
	default:
		return loadSkillDocumentFromJSON(path)
	}
}

func loadSkillDocumentFromJSON(path string) (types.SkillDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return types.SkillDocument{}, err
	}
	var doc types.SkillDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return types.SkillDocument{}, err
	}
	return doc, nil
}

func loadSkillDocumentFromYAML(path string) (types.SkillDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return types.SkillDocument{}, err
	}
	var doc types.SkillDocument
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return types.SkillDocument{}, err
	}
	return doc, nil
}

func loadSkillDocumentFromZip(path string) (types.SkillDocument, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return types.SkillDocument{}, err
	}
	defer r.Close()

	for _, file := range r.File {
		ext := strings.ToLower(filepath.Ext(file.Name))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return types.SkillDocument{}, err
		}
		buf := &bytes.Buffer{}
		if _, err := buf.ReadFrom(rc); err != nil {
			rc.Close()
			return types.SkillDocument{}, err
		}
		_ = rc.Close()

		var doc types.SkillDocument
		if ext == ".json" {
			err = json.Unmarshal(buf.Bytes(), &doc)
		} else {
			err = yaml.Unmarshal(buf.Bytes(), &doc)
		}
		if err != nil {
			return types.SkillDocument{}, err
		}
		return doc, nil
	}

	return types.SkillDocument{}, fmt.Errorf("no supported skill document found in archive")
}

func waitForImport(req skillImportRequest) bool {
	return req.Wait == nil || *req.Wait
}

func (s *Server) startSkillImport(ctx context.Context, doc types.SkillDocument) (string, error) {
	if s.tasks == nil {
		return "", &types.AppError{Code: types.ErrInternal, Message: "task tracker not available"}
	}

	task, err := s.tasks.Create(ctx, "skill_import", map[string]interface{}{
		"name": doc.Name,
	})
	if err != nil {
		return "", err
	}

	go func(taskID string, skillDoc types.SkillDocument) {
		bg := context.Background()
		_ = s.tasks.Start(bg, taskID)
		meta, err := s.skills.Add(bg, skillDoc)
		if err != nil {
			_ = s.tasks.Fail(bg, taskID, err)
			return
		}
		_ = s.tasks.Complete(bg, taskID, map[string]interface{}{
			"skill_id": meta.ID,
			"name":     meta.Name,
		})
	}(task.ID, doc)

	return task.ID, nil
}
