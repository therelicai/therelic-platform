FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/relic-api ./cmd/relic-api
RUN CGO_ENABLED=0 go build -o /bin/relic-governance ./cmd/relic-governance

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /bin/relic-api /bin/relic-api
COPY --from=builder /bin/relic-governance /bin/relic-governance
EXPOSE 8080
CMD ["/bin/relic-api"]
