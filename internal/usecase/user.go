package usecase

import (
	"context"
	"fmt"

	"pg-shopee/internal/domain"
)

type UserRepository interface {
	Create(context.Context, domain.User) error
	Get(context.Context, string) (domain.User, error)
	List(context.Context) ([]domain.User, error)
}

type UserService struct {
	repository UserRepository
	newID      func() string
}

func NewUserService(repository UserRepository, newID func() string) *UserService {
	return &UserService{repository: repository, newID: newID}
}

func (s *UserService) Create(ctx context.Context, name, email string) (domain.User, error) {
	user, err := domain.NewUser(s.newID(), name, email)
	if err != nil {
		return domain.User{}, err
	}
	if err := s.repository.Create(ctx, user); err != nil {
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}
	return user, nil
}

func (s *UserService) Get(ctx context.Context, id string) (domain.User, error) {
	return s.repository.Get(ctx, id)
}

func (s *UserService) List(ctx context.Context) ([]domain.User, error) {
	return s.repository.List(ctx)
}
