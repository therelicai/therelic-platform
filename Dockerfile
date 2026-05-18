FROM golang:1.23-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /bin/relic-api ./cmd/relic-api
RUN CGO_ENABLED=0 go build -o /bin/relic-governance ./cmd/relic-governance

FROM alpine:3.20
# postgresql-client provides pg_dump and psql for `relic-api backup`
# and `restore`. wget is needed by the healthcheck in docker-compose.
RUN apk add --no-cache ca-certificates tzdata postgresql-client wget
# Migrations bundled into the image so `relic-api migrate up` works
# inside the container without an extra volume mount. The migrate
# subcommand reads from /migrations* by default; override with
# RELIC_MIGRATIONS_*_DIR if you mount them somewhere else.
COPY --from=builder /app/migrations /migrations
COPY --from=builder /app/migrations.supabase /migrations.supabase
COPY --from=builder /app/migrations.rls /migrations.rls
COPY --from=builder /bin/relic-api /bin/relic-api
COPY --from=builder /bin/relic-governance /bin/relic-governance
EXPOSE 8080
CMD ["/bin/relic-api"]
