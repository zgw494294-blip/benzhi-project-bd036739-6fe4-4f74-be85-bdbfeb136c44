package release

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"caption-release-workbench/internal/domain"
)

type ExportedPackage struct {
	Bytes  []byte `json:"-"`
	Digest string `json:"digest"`
}

func BuildPackage(manifest domain.FrozenManifest) (ExportedPackage, error) {
	digest, err := ManifestDigest(manifest)
	if err != nil || digest != manifest.ManifestDigest {
		return ExportedPackage{}, errors.New("冻结清单摘要校验失败")
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return ExportedPackage{}, err
	}
	meta := struct {
		ProjectID        string `json:"projectId"`
		Version          int64  `json:"version"`
		ManifestDigest   string `json:"manifestDigest"`
		NormalizedDigest string `json:"normalizedDigest"`
	}{manifest.ProjectID, manifest.ProjectVersion, manifest.ManifestDigest, validatorDigest(manifest.NormalizedVTT)}
	metaJSON, _ := json.Marshal(meta)
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	files := []struct {
		name string
		data []byte
	}{{"manifest.json", manifestJSON}, {"normalized.vtt", []byte(manifest.NormalizedVTT)}, {"metadata.json", metaJSON}}
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Store}
		h.SetModTime(time.Unix(0, 0))
		w, e := zw.CreateHeader(h)
		if e != nil {
			return ExportedPackage{}, e
		}
		if _, e = w.Write(f.data); e != nil {
			return ExportedPackage{}, e
		}
	}
	if err := zw.Close(); err != nil {
		return ExportedPackage{}, err
	}
	sum := sha256.Sum256(b.Bytes())
	return ExportedPackage{Bytes: b.Bytes(), Digest: hex.EncodeToString(sum[:])}, nil
}

func validatorDigest(vtt string) string {
	sum := sha256.Sum256([]byte(vtt))
	return hex.EncodeToString(sum[:])
}
