# Tensor-Core - Go / Gin. Multi-stage: build a static binary, ship it on a
# minimal base. Migrations are embedded in the binary (internal/db/migrations).
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/api /app/api
COPY --from=build /out/seed /app/seed
COPY --from=build /out/migrate /app/migrate
EXPOSE 8001
USER nonroot:nonroot

# One binary, and it migrates itself.
#
# The obvious shape - ENTRYPOINT ["/app/migrate && /app/api"] - cannot work
# here: the base is distroless, so there is no shell to chain them, and an
# exec-form ENTRYPOINT runs exactly one program. /app/migrate and /app/seed
# still ship for running by hand.
#
# So the API applies pending migrations itself at startup, before it opens the
# pool. Without that, every deploy shipped a binary built from current code
# against whatever schema the database last had, and one missing column made
# every `SELECT *` fail instantly - a 500 in about a millisecond with nothing
# in the log. RUN_MIGRATIONS=false opts out where a separate step owns the
# schema, or where replicas must not race to migrate.
ENTRYPOINT ["/app/api"]
