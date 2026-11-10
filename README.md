# pg-shopee

Go Fiber v3 starter with Clean Architecture and a sample user API.

## Run

Use Go 1.27.3 or newer, matching `go.mod` and `go.work`.

```sh
go mod download
go run ./cmd/api
```

The server listens on `:8080`. Set `HTTP_ADDRESS` and `SHUTDOWN_TIMEOUT`
in the process environment to override defaults. `.env.example` documents
these values; `.env` files are not loaded automatically.

```sh
HTTP_ADDRESS=:3000 SHUTDOWN_TIMEOUT=5s go run ./cmd/api
```

## Structure

```text
cmd/api/                 Composition root: configuration and dependency wiring
internal/domain/         Entities and business validation; standard library only
internal/usecase/        Application operations and repository interface
internal/adapter/http/  Fiber routes, request/response types, error mapping
internal/adapter/memory/ Concurrent in-memory repository
internal/config/         Environment configuration
```

Dependencies point inward: adapters depend on use cases and domain; domain
and use cases do not import Fiber or a database driver. The composition root
injects the repository and ID generator into the service. To add a database,
implement `usecase.UserRepository` in another adapter and change the wiring
in `cmd/api/main.go`.

The user API is a working example. Storage resets on restart, and each server
process has its own data. Authentication, persistent storage, and Shopee
integration are not included.

## Endpoints

| Method | Path | Response |
| --- | --- | --- |
| GET | `/healthz` | Process liveness |
| POST | `/api/v1/users` | Create a user; returns 201 and a Location header |
| GET | `/api/v1/users` | List users (empty list is `[]`) |
| GET | `/api/v1/users/:id` | Get a user or return 404 |

```sh
curl http://localhost:8080/healthz
curl -i http://localhost:8080/api/v1/users \
  -H 'Content-Type: application/json' \
  -d '{"name":"Ada","email":"ada@example.com"}'
curl http://localhost:8080/api/v1/users
```

Names are trimmed; emails are trimmed, lowercased, validated, and unique.
Errors use `{"error":"message"}` with 400 for invalid input, 404 for a missing
user, and 409 for a duplicate email. Unexpected errors are logged and return
a generic 500 response. Responses include an `X-Request-ID` header.

SIGINT and SIGTERM trigger graceful shutdown with the configured timeout.

## Checks

```sh
make test
make vet
make build
```

Fiber API references: [routing and testing](https://docs.gofiber.io/api/app/),
[binding](https://docs.gofiber.io/api/bind/).
