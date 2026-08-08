# Tensor-Core - Go / Gin. Multi-stage: build a static binary, ship it on a slim
# Debian base that also carries OpenSCAD, which the personalisation endpoints shell
# out to (the name is rendered to STL headlessly). Migrations are embedded in the
# binary (internal/db/migrations).
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

# Runtime: slim Debian + OpenSCAD (and a font for extruded text) + CA certs for the
# outbound TLS to Neon, S3 and OpenRouter. QT_QPA_PLATFORM=offscreen lets OpenSCAD
# export STL without an X server.
FROM debian:12-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      openscad ca-certificates \
      fonts-liberation fonts-dejavu fonts-freefont-ttf \
    && rm -rf /var/lib/apt/lists/*
ENV QT_QPA_PLATFORM=offscreen OPENSCAD_BIN=openscad
WORKDIR /app
COPY --from=build /out/api /app/api
COPY --from=build /out/seed /app/seed
COPY --from=build /out/migrate /app/migrate
EXPOSE 8001
ENTRYPOINT ["/app/api"]
