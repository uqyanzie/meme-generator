package auth

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"

	"github.com/nyxsr/meme-portal/api/internal/platform/token"
)

// Service logika bisnis auth.
type Service struct {
	users  *Repository
	tokens *token.Manager
}

func NewService(users *Repository, tokens *token.Manager) *Service {
	return &Service{users: users, tokens: tokens}
}

func (s *Service) Register(ctx context.Context, dto RegisterDTO) (AuthResponse, error) {
	existing, err := s.users.GetByEmail(ctx, dto.Email)
	if err == nil && existing.ID != "" {
		return AuthResponse{}, ErrEmailTaken
	}
	hash, err := HashPassword(dto.Password)
	if err != nil {
		return AuthResponse{}, fmt.Errorf("auth service: hash gagal: %w", err)
	}
	user, err := s.users.Create(ctx, User{Email: dto.Email, Name: dto.Name, PasswordHash: hash})
	if err != nil {
		return AuthResponse{}, err
	}
	return s.issue(ctx, user)
}

func (s *Service) Login(ctx context.Context, dto LoginDTO) (AuthResponse, error) {
	user, err := s.users.GetByEmail(ctx, dto.Email)
	if err != nil {
		return AuthResponse{}, ErrInvalidCredential
	}
	if !VerifyPassword(user.PasswordHash, dto.Password) {
		return AuthResponse{}, ErrInvalidCredential
	}
	return s.issue(ctx, user)
}

func (s *Service) Refresh(ctx context.Context, raw string) (AuthResponse, error) {
	claims, err := s.tokens.VerifyRefresh(ctx, raw)
	if err != nil {
		return AuthResponse{}, ErrInvalidToken
	}
	user, err := s.users.GetByID(ctx, claims.UserID)
	if err != nil {
		return AuthResponse{}, ErrUserNotFound
	}
	return s.issue(ctx, user)
}

func (s *Service) Me(ctx context.Context, userID string) (UserDTO, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return UserDTO{}, ErrUserNotFound
	}
	return toDTO(user), nil
}

func (s *Service) Logout(ctx context.Context, accessRaw, refreshRaw string) error {
	var errs []error
	var revokedUserID string

	if accessRaw != "" {
		if claims, err := s.tokens.ParseAccess(accessRaw); err == nil {
			if claims.ExpiresAt != nil {
				if err := s.tokens.RevokeAccess(ctx, claims.ID, claims.ExpiresAt.Time); err != nil {
					errs = append(errs, err)
				}
			}
			if err := s.tokens.Revoke(ctx, claims.UserID); err != nil {
				errs = append(errs, err)
			}
			revokedUserID = claims.UserID
		}
	}

	if refreshRaw != "" {
		if claims, err := s.tokens.VerifyRefresh(ctx, refreshRaw); err == nil && claims.UserID != revokedUserID {
			if err := s.tokens.Revoke(ctx, claims.UserID); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)
}

func (s *Service) issue(ctx context.Context, user User) (AuthResponse, error) {
	pair, err := s.tokens.Issue(ctx, user.ID, user.Role)
	if err != nil {
		return AuthResponse{}, err
	}
	return AuthResponse{
		User: toDTO(user),
		Token: TokenPayload{
			AccessToken:  pair.AccessToken,
			RefreshToken: pair.RefreshToken,
			ExpiresIn:    pair.ExpiresIn,
		},
	}, nil
}

func toDTO(u User) UserDTO {
	return UserDTO{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role}
}

// HashPassword memakai argon2id (format PHC string disederhanakan).
func HashPassword(password string) (string, error) {
	salt := []byte(token.RandomHex(16))
	const (
		timeCost = 1
		memory   = 64 * 1024
		threads  = 4
		keyLen   = 32
	)
	key := argon2.IDKey([]byte(password), salt, timeCost, memory, uint8(threads), keyLen)
	return fmt.Sprintf("argon2id$%d$%d$%d$%s$%s", timeCost, memory, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func VerifyPassword(encoded, password string) bool {
	// Format: argon2id$time$memory$threads$salt$key (6 bagian, sama dengan
	// output HashPassword di atas).
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "argon2id" {
		return false
	}
	var timeCost, memory, threads uint32
	if _, err := fmt.Sscanf(parts[1]+" "+parts[2]+" "+parts[3], "%d %d %d", &timeCost, &memory, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	computed := argon2.IDKey([]byte(password), salt, timeCost, memory, uint8(threads), uint32(len(key)))
	return subtle.ConstantTimeCompare(computed, key) == 1
}
