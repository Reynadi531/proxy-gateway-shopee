package domain

import (
	"errors"
	"net/mail"
	"strings"
)

var (
	ErrInvalidUser  = errors.New("name and a valid email are required")
	ErrUserNotFound = errors.New("user not found")
	ErrEmailTaken   = errors.New("email already exists")
)

type User struct {
	ID    string
	Name  string
	Email string
}

func NewUser(id, name, email string) (User, error) {
	name = strings.TrimSpace(name)
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if id == "" || name == "" || err != nil || address.Address != email {
		return User{}, ErrInvalidUser
	}
	return User{ID: id, Name: name, Email: email}, nil
}
