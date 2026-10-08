package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// MailLog is one send attempt, kept so an operator can answer "did the reset
// mail go out?" without reading server logs.
//
// What it does not hold is as deliberate as what it does: no subject, no
// body, no template data. A reset code travels through the mailer as data, so
// a field for the data would be a field that could hold a credential. Error
// is the one free-text field and is short, single-line and scrubbed before it
// is stored.
//
// Rows expire on their own (see the TTL index on created_at).
type MailLog struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
	Template  string             `bson:"template" json:"template"`
	// To is the recipient, stored in full.
	To     string `bson:"to" json:"to"`
	Status string `bson:"status" json:"status"`
	Error  string `bson:"error,omitempty" json:"error,omitempty"`
	// Source is "system" for mail tenantcore sends itself, or the service
	// client's name for mail sent through /svc/notifications/email.
	Source   string `bson:"source" json:"source"`
	TenantID string `bson:"tenant_id,omitempty" json:"tenant_id,omitempty"`
}
