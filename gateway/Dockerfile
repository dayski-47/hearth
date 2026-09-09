# syntax=docker/dockerfile:1

FROM golang:1.27-bookworm AS build
WORKDIR /src

# Codegen tools, pinned to the versions ci.yml uses.
RUN go install github.com/bufbuild/buf/cmd/buf@v1.47.2 \
 && go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.27.0

# Module cache: copy manifests first.
COPY gateway/go.mod gateway/go.sum ./gateway/
RUN cd gateway && go mod download

# Sources needed for codegen + build.
COPY proto ./proto
COPY buf.yaml buf.gen.yaml ./
COPY gateway ./gateway

RUN buf generate \
 && (cd gateway/internal/store && sqlc generate) \
 && cd gateway && CGO_ENABLED=0 GOFLAGS=-trimpath \
      go build -ldflags='-s -w' -o /out/hearth-gateway ./cmd/hearth-gateway

FROM node:22-bookworm AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web ./
RUN npm run build

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/hearth-gateway /app/hearth-gateway
COPY --from=web /web/dist /app/web
ENV HEARTH_WEB_DIR=/app/web
EXPOSE 8080 9090
ENTRYPOINT ["/app/hearth-gateway"]
CMD ["serve"]
