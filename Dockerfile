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
ENTRYPOINT ["/app/api"]
