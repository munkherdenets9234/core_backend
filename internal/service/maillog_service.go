package service

import (
	"context"
	"strings"

	"github.com/eandstravel/tenantcore/internal/models"
	"github.com/eandstravel/tenantcore/pkg/apierr"
	"github.com/eandstravel/tenantcore/pkg/mailer"
)

// MailLogMaxLimit is the largest page the mail log list will serve.
const MailLogMaxLimit = 100

// mailLogStore is the slice of the repository the service needs, narrow so
// the validation can be tested without MongoDB.
type mailLogStore interface {
	List(ctx context.Context, status, template string, page, limit int) ([]*models.MailLog, int64, error)
}

// MailLogService reads the mail log for the console. Writing is the mailer's
// job, through mailer.Recorder; nothing here can add or change a row.
type MailLogService struct {
	repo mailLogStore
}

func NewMailLogService(repo mailLogStore) *MailLogService {
	return &MailLogService{repo: repo}
}

// List returns one page of the log, newest first.
//
// status and template come straight from the query string, so both are
// checked against the closed sets they can take. An empty value means "any".
// A value is never passed to the database unless it is one we know, which is
// also why the error text can stay generic.
func (s *MailLogService) List(ctx context.Context, status, template string, page, limit int) ([]*models.MailLog, int64, error) {
	status = strings.TrimSpace(status)
	template = strings.TrimSpace(template)

	if status != "" && status != mailer.StatusSent && status != mailer.StatusFailed {
		return nil, 0, apierr.ValidationFailed("status must be one of: sent, failed")
	}
	if template != "" {
		if _, ok := mailer.Known(template); !ok {
			return nil, 0, apierr.ValidationFailed("unknown template")
		}
	}
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > MailLogMaxLimit {
		limit = 20
	}

	out, total, err := s.repo.List(ctx, status, template, page, limit)
	if err != nil {
		return nil, 0, apierr.Internal(err)
	}
	return out, total, nil
}
