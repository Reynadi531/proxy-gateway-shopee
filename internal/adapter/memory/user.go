package memory

import (
	"context"
	"sort"
	"sync"

	"pg-shopee/internal/domain"
)

type UserRepository struct {
	mu     sync.RWMutex
	users  map[string]domain.User
	emails map[string]struct{}
}

func NewUserRepository() *UserRepository {
	return &UserRepository{users: make(map[string]domain.User), emails: make(map[string]struct{})}
}

func (r *UserRepository) Create(ctx context.Context, user domain.User) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.emails[user.Email]; exists {
		return domain.ErrEmailTaken
	}
	r.users[user.ID] = user
	r.emails[user.Email] = struct{}{}
	return nil
}

func (r *UserRepository) Get(ctx context.Context, id string) (domain.User, error) {
	if err := ctx.Err(); err != nil {
		return domain.User{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	user, exists := r.users[id]
	if !exists {
		return domain.User{}, domain.ErrUserNotFound
	}
	return user, nil
}

func (r *UserRepository) List(ctx context.Context) ([]domain.User, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	users := make([]domain.User, 0, len(r.users))
	for _, user := range r.users {
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	return users, nil
}
