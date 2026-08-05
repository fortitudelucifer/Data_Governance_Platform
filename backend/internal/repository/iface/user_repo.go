package iface

import (
	"context"

	dbmodel "text-annotation-platform/internal/model/relational"
)

// DBUserRepo is the minimal relational-DB interface required by AuthService and
// UserService. The concrete implementation is *repository.DB, which
// satisfies this interface with zero runtime changes.
type DBUserRepo interface {
	// FindUserByUsername looks up a user by their login name.
	FindUserByUsername(ctx context.Context, username string) (*dbmodel.User, error)

	// FindUserByID looks up a user by primary key.
	FindUserByID(ctx context.Context, id uint) (*dbmodel.User, error)

	// CreateUser inserts a new user record.
	CreateUser(ctx context.Context, user *dbmodel.User) error

	// ListUsersWithCount returns a page of users and the total count.
	ListUsersWithCount(ctx context.Context, page, pageSize int) ([]dbmodel.User, int64, error)

	// SaveUser persists all fields of an existing user record.
	SaveUser(ctx context.Context, user *dbmodel.User) error

	// UpdateUserFields applies a partial update identified by id.
	// Returns the number of rows affected and any error.
	UpdateUserFields(ctx context.Context, id uint, updates map[string]interface{}) (int64, error)

	// DeleteUser removes a user by id. Returns rows affected and any error.
	DeleteUser(ctx context.Context, id uint) (int64, error)
}
