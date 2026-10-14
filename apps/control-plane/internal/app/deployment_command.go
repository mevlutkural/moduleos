package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/distribution/reference"
	"github.com/google/uuid"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/store"
	"github.com/mevlutkural/moduleos/apps/control-plane/internal/swarm"
)

type DeploymentMutationRequest struct {
	AppName            string
	ExpectedAppID      string
	ExpectedGeneration int64
	IdempotencyKey     string
	Operation          store.DeploymentOperation
	Image              string
	RollbackTargetID   string
}

func (s *Service) AcceptDeployment(ctx context.Context, request DeploymentMutationRequest) (*store.DeploymentAcceptance, error) {
	if err := validateName(request.AppName); err != nil {
		return nil, err
	}
	if !canonicalDeploymentUUID(request.ExpectedAppID) || request.ExpectedGeneration <= 0 {
		return nil, fmt.Errorf("%w: invalid application precondition", store.ErrInvalidData)
	}
	if !validIdempotencyKey(request.IdempotencyKey) {
		return nil, fmt.Errorf("%w: invalid idempotency key", store.ErrInvalidData)
	}
	var fingerprintValue string
	switch request.Operation {
	case store.DeploymentOperationDeploy:
		if request.RollbackTargetID != "" || !validDeploymentImageReference(request.Image) {
			return nil, fmt.Errorf("%w: invalid deploy image", store.ErrInvalidData)
		}
		parsed, err := reference.ParseNormalizedNamed(request.Image)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid deploy image", store.ErrInvalidData)
		}
		request.Image = reference.FamiliarString(reference.TagNameOnly(parsed))
		fingerprintValue = request.Image
	case store.DeploymentOperationRollback:
		if request.Image != "" || !canonicalDeploymentUUID(request.RollbackTargetID) {
			return nil, fmt.Errorf("%w: invalid rollback target", store.ErrInvalidData)
		}
		fingerprintValue = request.RollbackTargetID
	default:
		return nil, fmt.Errorf("%w: invalid deployment operation", store.ErrInvalidData)
	}
	keyDigest := sha256.Sum256([]byte(request.IdempotencyKey))
	fingerprint := deploymentRequestFingerprint(request, fingerprintValue)
	if err := s.lockResources(ctx); err != nil {
		return nil, err
	}
	defer s.resourceMu.Unlock()
	now := s.deploymentClock().UTC()
	application, err := s.store.GetApplication(ctx, request.AppName)
	if err != nil {
		return nil, err
	}
	if application.ID != request.ExpectedAppID {
		return nil, store.ErrGenerationConflict
	}
	replay, err := s.store.LookupDeploymentCommand(ctx, application.ID, keyDigest, fingerprint, now)
	if err != nil || replay != nil {
		return replay, err
	}
	if application.DesiredGeneration != request.ExpectedGeneration {
		return nil, store.ErrGenerationConflict
	}
	if application.DesiredGeneration == math.MaxInt64 {
		return nil, fmt.Errorf("%w: generation overflow", store.ErrInvalidData)
	}
	if application.DeletionTimestamp != nil {
		return nil, store.ErrConflict
	}
	if request.Operation == store.DeploymentOperationRollback {
		target, err := s.store.GetDeploymentForApp(ctx, application.ID, request.RollbackTargetID)
		if err != nil {
			return nil, err
		}
		if (target.Status != store.DeploymentStatusSucceeded && target.Status != store.DeploymentStatusSuccess) || !swarm.IsImmutableImageReference(target.ResolvedImage) {
			return nil, store.ErrConflict
		}
		request.Image = target.ResolvedImage
	}
	candidate := *application
	candidate.Image = request.Image
	candidate.DesiredGeneration++
	if _, err := s.buildDesiredServiceSpec(ctx, &candidate); err != nil {
		return nil, fmt.Errorf("%w: %v", store.ErrInvalidData, err)
	}
	command := store.DeploymentCommand{
		AppName: request.AppName, ExpectedAppID: request.ExpectedAppID,
		ExpectedGeneration: request.ExpectedGeneration, Operation: request.Operation,
		Image: request.Image, RollbackTargetID: request.RollbackTargetID,
		NewDeploymentID: uuid.NewString(), KeyDigest: keyDigest,
		Fingerprint: fingerprint, Now: now,
		ConvergenceDeadline: now.Add(s.deploymentTimeout),
	}
	accepted, err := s.store.AcceptDeploymentCommand(ctx, command)
	if err != nil {
		return accepted, err
	}
	if !accepted.Replayed {
		s.enqueue(request.AppName)
	}
	return accepted, nil
}

func validDeploymentImageReference(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) != value || value == "" || len(value) > 255 {
		return false
	}
	if len(value) >= 64 && deploymentLowerHex(value) {
		return false
	}
	if separator := strings.LastIndexByte(value, '@'); separator >= 0 {
		digest := value[separator+1:]
		if len(digest) != len("sha256:")+64 || !strings.HasPrefix(digest, "sha256:") || !deploymentLowerHex(digest[len("sha256:"):]) {
			return false
		}
	}
	_, err := reference.ParseNormalizedNamed(value)
	return err == nil
}

func deploymentLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validIdempotencyKey(key string) bool {
	if len(key) < 16 || len(key) > 128 {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '.' || c == '_' || c == ':' || c == '-') {
			return false
		}
	}
	return true
}

func deploymentRequestFingerprint(request DeploymentMutationRequest, value string) [32]byte {
	var input bytes.Buffer
	for _, field := range []string{
		"moduleos/deployment-command/v1", string(request.Operation), request.ExpectedAppID,
		fmt.Sprint(request.ExpectedGeneration), value,
	} {
		_ = binary.Write(&input, binary.BigEndian, uint32(len(field)))
		_, _ = input.WriteString(field)
	}
	return sha256.Sum256(input.Bytes())
}

func (s *Service) WithDeploymentClock(clock func() time.Time) *Service {
	if clock != nil {
		s.deploymentClock = clock
	}
	return s
}
