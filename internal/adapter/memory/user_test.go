package memory_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"pg-shopee/internal/adapter/memory"
	"pg-shopee/internal/domain"

	"github.com/google/uuid"
)

func TestConcurrentEmailUniqueness(t *testing.T) {
	repo := memory.NewUserRepository()
	var wg sync.WaitGroup
	var created atomic.Int32
	for range 20 {
		wg.Go(func() {
			err := repo.Create(context.Background(), domain.User{ID: uuid.NewString(), Name: "Ada", Email: "ada@example.com"})
			if err == nil {
				created.Add(1)
			} else if !errors.Is(err, domain.ErrEmailTaken) {
				t.Errorf("create: %v", err)
			}
			if _, err := repo.List(context.Background()); err != nil {
				t.Errorf("list: %v", err)
			}
		})
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatalf("created %d users with the same email", created.Load())
	}
}
