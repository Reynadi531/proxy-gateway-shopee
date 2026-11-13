package domain

import (
	"errors"
	"time"
)

var (
	// ErrReloginRequired means the Shopee account session can no longer be
	// renewed silently and an operator must log in again with an OTP.
	ErrReloginRequired = errors.New("Shopee login with OTP required")
	ErrNoPendingLogin  = errors.New("no OTP login in progress; start with /login")
	ErrSessionReplaced = errors.New("session file changed during renewal; renewal discarded")
)

// SessionInfo describes the persisted Shopee session without exposing credentials.
type SessionInfo struct {
	MerchantID   string
	MerchantName string
	StoreID      string
	// TokenPresent reports whether the file holds a merchant token for the account.
	TokenPresent bool
	// ExpiresAt is when the merchant token stops being usable; zero when unknown.
	ExpiresAt time.Time
	// Renewable reports whether the session can be renewed without an OTP.
	Renewable bool
}

// OTPRequest describes a sent Shopee OTP.
type OTPRequest struct {
	Channel   int
	ExpiresAt time.Time
}
