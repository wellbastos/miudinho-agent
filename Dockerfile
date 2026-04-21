FROM golang:1.25.9 AS build
WORKDIR /src
COPY . .
RUN go mod tidy && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/manager ./cmd/manager

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
