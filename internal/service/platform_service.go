package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/internal/repository"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/apikey"
	"github.com/eandstravel/tenantcore/pkg/password"
	"github.com/eandstravel/tenantcore/pkg/token"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// ── Platform users ────────────────────────────────────────────────────────

type PlatformUserService struct {
	repo  *repository.PlatformUserRepo
	maker *token.Maker
	ttl   time.Duration
}

func NewPlatformUserService(repo *repository.PlatformUserRepo, maker *token.Maker, ttl time.Duration) *PlatformUserService {
	return &PlatformUserService{repo: repo, maker: maker, ttl: ttl}
}

// Login verifies the password and mints an Ed25519 token.
//
// This is the point of centralising identity: the token minted here is
// verifiable by every product service using only the public key, so a
// superadmin signs in once and is recognised everywhere, and no product can
// forge a token for any other.
//
// Unknown email and wrong password give the same answer for the same reason
// they always should — the difference is an account-existence oracle. A
// suspended account does say so, because that is a state the person can act
// on and they have already proved they hold the password.
func (s *PlatformUserService) Login(ctx context.Context, email, rawPassword string) (string, *models.PlatformUser, error) {
	email = normaliseEmail(email)

	u, err := s.repo.FindByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// Still spend the time a real verification costs. Returning
			// instantly for an unknown address is a timing oracle that gives
			// back exactly what the identical message was hiding.
			password.DummyCompare()
			return "", nil, apierr.Unauthorized("invalid email or password")
		}
		return "", nil, apierr.Internal(err)
	}

	if !password.Verify(u.PasswordHash, rawPassword) {
		return "", nil, apierr.Unauthorized("invalid email or password")
	}
	if u.Status != models.PlatformUserActive {
		return "", nil, apierr.Forbidden("account suspended")
	}

	// Platform users are never tenant-scoped; token.Claims.Valid enforces it
	// at mint time, so an impossible token cannot be created here even by
	// mistake.
	signed, _, err := s.maker.Create(u.ID.Hex(), token.RoleSuperadmin, "", s.ttl)
	if err != nil {
		return "", nil, apierr.Internal(err)
	}
	return signed, u, nil
}

// EnsureBootstrap creates the first platform user from startup configuration
// if no account with that email exists yet.
//
// It never overwrites an existing account's password: restarting with
// different environment values must not silently change who can log in, and
// an env file left in place is not consent to reset an administrator.
func (s *PlatformUserService) EnsureBootstrap(ctx context.Context, name, email, rawPassword string) error {
	if email == "" || rawPassword == "" {
		return nil
	}
	email = normaliseEmail(email)

	if _, err := s.repo.FindByEmail(ctx, email); err == nil {
		return nil
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return err
	}

	hash, err := password.Hash(rawPassword)
	if err != nil {
		return err
	}
	return s.repo.Create(ctx, &models.PlatformUser{Name: name, Email: email, PasswordHash: hash})
}

// Create adds another platform user. A blank password means one is generated
// and returned — the only moment it exists in plaintext.
func (s *PlatformUserService) Create(ctx context.Context, name, email, rawPassword string) (*models.PlatformUser, string, error) {
	email = normaliseEmail(email)
	if email == "" {
		return nil, "", apierr.BadRequest("email is required")
	}

	generated := rawPassword == ""
	if generated {
		var err error
		rawPassword, err = password.GenerateRandom()
		if err != nil {
			return nil, "", apierr.Internal(err)
		}
	} else if len(rawPassword) < 8 {
		return nil, "", apierr.BadRequest("password must be at least 8 characters")
	}

	hash, err := password.Hash(rawPassword)
	if err != nil {
		return nil, "", apierr.Internal(err)
	}

	u := &models.PlatformUser{Name: name, Email: email, PasswordHash: hash}
	if err := s.repo.Create(ctx, u); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, "", apierr.Conflict("a platform user with this email already exists")
		}
		return nil, "", apierr.Internal(err)
	}
	if !generated {
		rawPassword = ""
	}
	return u, rawPassword, nil
}

func (s *PlatformUserService) List(ctx context.Context, page, limit int) ([]*models.PlatformUser, int64, error) {
	out, total, err := s.repo.List(ctx, page, limit)
	if err != nil {
		return nil, 0, apierr.Internal(err)
	}
	return out, total, nil
}

// UpdateStatus refuses to suspend the last active platform user.
//
// tenantcore administers every tenant on the platform. Locking every
// administrator out of it is not a mistake that can be undone through the
// API, and "are you sure" in a console is not a constraint.
func (s *PlatformUserService) UpdateStatus(ctx context.Context, idStr string, status models.PlatformUserStatus) error {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return apierr.BadRequest("invalid user id")
	}
	switch status {
	case models.PlatformUserActive, models.PlatformUserSuspended:
	default:
		return apierr.BadRequest("status must be active or suspended")
	}

	if status == models.PlatformUserSuspended {
		u, err := s.repo.FindByID(ctx, id)
		if err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return apierr.NotFound("platform user")
			}
			return apierr.Internal(err)
		}
		if u.Status == models.PlatformUserActive {
			active, err := s.repo.CountActive(ctx)
			if err != nil {
				return apierr.Internal(err)
			}
			if active <= 1 {
				return apierr.Conflict("cannot suspend the last active platform user")
			}
		}
	}

	if err := s.repo.UpdateStatus(ctx, id, status); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("platform user")
		}
		return apierr.Internal(err)
	}
	return nil
}

// ChangePassword is the caller changing their own, proving the current one.
func (s *PlatformUserService) ChangePassword(ctx context.Context, id primitive.ObjectID, current, next string) error {
	if len(next) < 8 {
		return apierr.BadRequest("new password must be at least 8 characters")
	}
	u, err := s.repo.FindByID(ctx, id)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("platform user")
		}
		return apierr.Internal(err)
	}
	if !password.Verify(u.PasswordHash, current) {
		return apierr.Unauthorized("current password is incorrect")
	}

	hash, err := password.Hash(next)
	if err != nil {
		return apierr.Internal(err)
	}
	if err := s.repo.UpdatePassword(ctx, id, hash); err != nil {
		return apierr.Internal(err)
	}
	return nil
}

// ResetPassword is a superadmin setting another account's password. A blank
// value generates one and returns it once.
func (s *PlatformUserService) ResetPassword(ctx context.Context, idStr, next string) (string, error) {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return "", apierr.BadRequest("invalid user id")
	}

	generated := next == ""
	if generated {
		next, err = password.GenerateRandom()
		if err != nil {
			return "", apierr.Internal(err)
		}
	} else if len(next) < 8 {
		return "", apierr.BadRequest("new password must be at least 8 characters")
	}

	hash, err := password.Hash(next)
	if err != nil {
		return "", apierr.Internal(err)
	}
	if err := s.repo.UpdatePassword(ctx, id, hash); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", apierr.NotFound("platform user")
		}
		return "", apierr.Internal(err)
	}
	if !generated {
		return "", nil
	}
	return next, nil
}

// ── Service clients ───────────────────────────────────────────────────────

// serviceClientStore is what ServiceClientService needs from storage, as an
// interface so the key lifecycle can be tested without a database.
// *repository.ServiceClientRepo satisfies it.
type serviceClientStore interface {
	Create(ctx context.Context, c *models.ServiceClient) error
	FindByKeyHash(ctx context.Context, hash string) (*models.ServiceClient, error)
	FindByID(ctx context.Context, id primitive.ObjectID) (*models.ServiceClient, error)
	List(ctx context.Context) ([]*models.ServiceClient, error)
	UpdateStatus(ctx context.Context, id primitive.ObjectID, status models.ServiceClientStatus) error
	ReplaceKey(ctx context.Context, id primitive.ObjectID, keyHash, keyLast4 string) (*models.ServiceClient, error)
	TouchLastSeen(ctx context.Context, id primitive.ObjectID) error
}

type ServiceClientService struct {
	repo serviceClientStore
}

func NewServiceClientService(repo *repository.ServiceClientRepo) *ServiceClientService {
	return &ServiceClientService{repo: repo}
}

// Create registers a product service and returns its key once.
func (s *ServiceClientService) Create(ctx context.Context, name string) (*models.ServiceClient, string, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return nil, "", apierr.BadRequest("name is required")
	}

	raw, hash, err := apikey.Generate()
	if err != nil {
		return nil, "", apierr.Internal(err)
	}

	c := &models.ServiceClient{Name: name, KeyHash: hash, KeyLast4: apikey.Last4(raw)}
	if err := s.repo.Create(ctx, c); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return nil, "", apierr.Conflict("a service client with this name already exists").In(apierr.DomainService)
		}
		return nil, "", apierr.Internal(err)
	}
	return c, raw, nil
}

// Authenticate resolves a service key on the machine-to-machine path.
//
// Reported under DomainService rather than DomainAuth so a dashboard can tell
// "a product service is calling with a bad key" — which means a deployment is
// misconfigured or a key was revoked without redeploying — apart from a human
// mistyping a password. Same status code, completely different incident.
func (s *ServiceClientService) Authenticate(ctx context.Context, rawKey string) (*models.ServiceClient, error) {
	c, err := s.repo.FindByKeyHash(ctx, apikey.Hash(rawKey))
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apierr.Unauthorized("unknown service key").In(apierr.DomainService)
		}
		return nil, apierr.Internal(err)
	}
	if c.Status != models.ServiceClientActive {
		return nil, apierr.Forbidden("service key revoked").In(apierr.DomainService)
	}

	// Best-effort and detached: this runs on the hot path of every
	// entitlement lookup, and a failed bookkeeping write must never turn a
	// successful authentication into a failed request.
	go func(id primitive.ObjectID) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.repo.TouchLastSeen(ctx, id)
	}(c.ID)

	return c, nil
}

func (s *ServiceClientService) List(ctx context.Context) ([]*models.ServiceClient, error) {
	out, err := s.repo.List(ctx)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return out, nil
}

func (s *ServiceClientService) Revoke(ctx context.Context, idStr string) error {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return apierr.BadRequest("invalid service client id")
	}
	if err := s.repo.UpdateStatus(ctx, id, models.ServiceClientRevoked); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return apierr.NotFound("service client").In(apierr.DomainService)
		}
		return apierr.Internal(err)
	}
	return nil
}

// Rotate replaces a service client's key and returns the new one once.
//
// It swaps the hash on the existing record rather than creating a second
// record and revoking the first: the name is uniquely indexed, and a single
// write is atomic by construction. If it fails the old key is still valid;
// if it succeeds the old key is dead at once. A revoked client is refused,
// because rotating would quietly bring it back to life under a new key.
func (s *ServiceClientService) Rotate(ctx context.Context, idStr string) (*models.ServiceClient, string, error) {
	id, err := primitive.ObjectIDFromHex(idStr)
	if err != nil {
		return nil, "", apierr.BadRequest("invalid service client id")
	}

	raw, hash, err := apikey.Generate()
	if err != nil {
		return nil, "", apierr.Internal(err)
	}

	c, err := s.repo.ReplaceKey(ctx, id, hash, apikey.Last4(raw))
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, "", apierr.Internal(err)
		}
		// No active record matched: tell "never existed" from "revoked".
		existing, ferr := s.repo.FindByID(ctx, id)
		if ferr != nil {
			if errors.Is(ferr, mongo.ErrNoDocuments) {
				return nil, "", apierr.NotFound("service client").In(apierr.DomainService)
			}
			return nil, "", apierr.Internal(ferr)
		}
		if existing.Status != models.ServiceClientActive {
			return nil, "", apierr.Conflict("service client is revoked").In(apierr.DomainService)
		}
		return nil, "", apierr.Internal(err)
	}

	// The record came back from the same write that swapped the key; reading it
	// again here could fail after the old key is already dead.
	return c, raw, nil
}

func normaliseEmail(e string) string {
	return strings.ToLower(strings.TrimSpace(e))
}

// DisplayName is the name of the platform user with this id, or "" when it
// cannot be read. It exists for attribution in notices, where a lookup that
// fails must cost the name and nothing else.
func (s *PlatformUserService) DisplayName(ctx context.Context, id primitive.ObjectID) string {
	u, err := s.repo.FindByID(ctx, id)
	if err != nil || u == nil {
		return ""
	}
	return u.Name
}
