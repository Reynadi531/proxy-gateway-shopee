FROM golang:1.27.3-alpine

ARG AIR_VERSION=v1.67.4

RUN apk add --no-cache ca-certificates git
RUN go install github.com/air-verse/air@${AIR_VERSION}

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

CMD ["air", "-c", ".air.toml"]
