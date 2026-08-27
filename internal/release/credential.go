package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"caption-release-workbench/internal/domain"
)

type VerificationCode string

const (
	VerificationOK             VerificationCode = "valid"
	VerificationMalformed      VerificationCode = "malformed"
	VerificationUnknownKey     VerificationCode = "unknown_key"
	VerificationBadSignature   VerificationCode = "invalid_signature"
	VerificationDigestMismatch VerificationCode = "digest_mismatch"
	VerificationStaleVersion   VerificationCode = "stale_version"
)

type Verification struct {
	Valid      bool               `json:"valid"`
	Code       VerificationCode   `json:"code"`
	Message    string             `json:"message"`
	Credential *domain.Credential `json:"credential,omitempty"`
}

type Service struct {
	mu      sync.RWMutex
	keyID   string
	private ed25519.PrivateKey
	public  map[string]ed25519.PublicKey
	now     func() time.Time
}

func New(keyID string, private ed25519.PrivateKey) (*Service, error) {
	if strings.TrimSpace(keyID) == "" {
		return nil, errors.New("keyID 不能为空")
	}
	if len(private) != ed25519.PrivateKeySize {
		return nil, errors.New("Ed25519 私钥长度无效")
	}
	pub := private.Public().(ed25519.PublicKey)
	return &Service{keyID: keyID, private: append(ed25519.PrivateKey(nil), private...), public: map[string]ed25519.PublicKey{keyID: append(ed25519.PublicKey(nil), pub...)}, now: time.Now}, nil
}

func Generate(keyID string) (*Service, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成签名密钥: %w", err)
	}
	return New(keyID, private)
}

func (s *Service) AddPublicKey(keyID string, public ed25519.PublicKey) error {
	if keyID == "" || len(public) != ed25519.PublicKeySize {
		return errors.New("公钥配置无效")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.public[keyID] = append(ed25519.PublicKey(nil), public...)
	return nil
}

func (s *Service) PublicKeyBase64() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return base64.RawURLEncoding.EncodeToString(s.public[s.keyID])
}

func (s *Service) Issue(projectID string, version int64, digest, issuer string) (domain.Credential, string, error) {
	if projectID == "" || version < 1 || len(digest) != sha256.Size*2 || issuer == "" {
		return domain.Credential{}, "", errors.New("凭据字段不完整")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	issuedAt := s.now().UTC().Truncate(time.Millisecond)
	credential := domain.Credential{
		CredentialID:   credentialID(projectID, version, digest),
		ProjectID:      projectID,
		ProjectVersion: version,
		ManifestDigest: digest,
		IssuedAt:       issuedAt,
		IssuerID:       issuer,
		KeyID:          s.keyID,
	}
	payload, err := signingPayload(credential)
	if err != nil {
		return domain.Credential{}, "", err
	}
	credential.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(s.private, payload))
	tokenBytes, err := json.Marshal(credential)
	if err != nil {
		return domain.Credential{}, "", fmt.Errorf("编码发布凭据: %w", err)
	}
	token := "CRW1." + base64.RawURLEncoding.EncodeToString(tokenBytes)
	return credential, token, nil
}

func Encode(credential domain.Credential) (string, error) {
	if credential.CredentialID == "" || credential.Signature == "" {
		return "", errors.New("凭据字段不完整")
	}
	data, err := json.Marshal(credential)
	if err != nil {
		return "", fmt.Errorf("编码发布凭据: %w", err)
	}
	return "CRW1." + base64.RawURLEncoding.EncodeToString(data), nil
}

func (s *Service) Verify(token, expectedProject, expectedDigest string, expectedVersion int64) Verification {
	credential, err := Decode(token)
	if err != nil {
		return Verification{Code: VerificationMalformed, Message: err.Error()}
	}
	s.mu.RLock()
	public, known := s.public[credential.KeyID]
	s.mu.RUnlock()
	if !known {
		return Verification{Code: VerificationUnknownKey, Message: "凭据引用了未知签名密钥", Credential: &credential}
	}
	signature, err := base64.RawURLEncoding.DecodeString(credential.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return Verification{Code: VerificationMalformed, Message: "签名编码格式错误", Credential: &credential}
	}
	payload, err := signingPayload(credential)
	if err != nil {
		return Verification{Code: VerificationMalformed, Message: err.Error(), Credential: &credential}
	}
	if !ed25519.Verify(public, payload, signature) {
		return Verification{Code: VerificationBadSignature, Message: "凭据签名验证失败", Credential: &credential}
	}
	if expectedProject != "" && credential.ProjectID != expectedProject || expectedDigest != "" && credential.ManifestDigest != expectedDigest {
		return Verification{Code: VerificationDigestMismatch, Message: "凭据与当前冻结清单不一致", Credential: &credential}
	}
	if expectedVersion > 0 && credential.ProjectVersion != expectedVersion {
		return Verification{Code: VerificationStaleVersion, Message: "凭据对应的项目版本已过期", Credential: &credential}
	}
	return Verification{Valid: true, Code: VerificationOK, Message: "签名、摘要及项目版本均有效", Credential: &credential}
}

func Decode(token string) (domain.Credential, error) {
	if !strings.HasPrefix(token, "CRW1.") {
		return domain.Credential{}, errors.New("凭据缺少 CRW1 版本前缀")
	}
	data, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "CRW1."))
	if err != nil || len(data) > 16*1024 {
		return domain.Credential{}, errors.New("凭据内容不是有效的 Base64URL")
	}
	var credential domain.Credential
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return domain.Credential{}, errors.New("凭据 JSON 格式错误")
	}
	if credential.CredentialID == "" || credential.ProjectID == "" || credential.ProjectVersion < 1 || len(credential.ManifestDigest) != 64 || credential.IssuedAt.IsZero() || credential.IssuerID == "" || credential.KeyID == "" || credential.Signature == "" {
		return domain.Credential{}, errors.New("凭据必填字段不完整")
	}
	return credential, nil
}

func signingPayload(credential domain.Credential) ([]byte, error) {
	credential.Signature = ""
	credential.PublishedAt = nil
	data, err := json.Marshal(credential)
	if err != nil {
		return nil, fmt.Errorf("编码签名载荷: %w", err)
	}
	return data, nil
}

func credentialID(projectID string, version int64, digest string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", projectID, version, digest)))
	return "cred_" + hex.EncodeToString(sum[:10])
}
