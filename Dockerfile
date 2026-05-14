FROM golang:1.25.10 AS build
WORKDIR /src

# Copiar arquivos de dependências primeiro para maximizar cache de camadas
COPY go.mod go.sum ./
COPY vendor/ vendor/

# Copiar o restante do código
COPY . .

# Build offline usando vendor — sem acesso à rede necessário
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -mod=vendor -trimpath -ldflags="-s -w" \
    -o /out/manager ./cmd/manager

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
